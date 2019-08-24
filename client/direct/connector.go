package direct

import (
	"strings"
	"time"

	"github.com/aclindsa/ofxgo"
	"github.com/johnstarich/sage/client/model"
	"github.com/johnstarich/sage/ledger"
	"github.com/johnstarich/sage/redactor"
	"github.com/pkg/errors"
	"github.com/shopspring/decimal"
)

const (
	ofxAuthFailed = 15500
)

var (
	// ErrAuthFailed is returned whenever a signon request fails with an authentication problem
	ErrAuthFailed = errors.New("Username or password is incorrect")
)

// Connector downloads statements directly from an institution's OFX/QFX API
type Connector interface {
	model.Institution

	URL() string
	Username() string
	Password() redactor.String
	SetPassword(redactor.String)
	Config() Config
}

// Requestor can annotate an ofxgo.Request to fetch statements
type Requestor interface {
	Statement(req *ofxgo.Request, start, end time.Time) error
}

type directConnect struct {
	model.BasicInstitution

	ConnectorURL      string
	ConnectorUsername string
	ConnectorPassword redactor.String `json:",omitempty"`
	ConnectorConfig   Config
}

// New creates an institution that can automatically download statements
func New(
	description,
	fid,
	org,
	url,
	username, password string,
	config Config,
) Connector {
	return &directConnect{
		BasicInstitution: model.BasicInstitution{
			InstDescription: description,
			InstFID:         fid,
			InstOrg:         org,
		},
		ConnectorConfig:   config,
		ConnectorPassword: redactor.String(password),
		ConnectorURL:      url,
		ConnectorUsername: username,
	}
}

func (d *directConnect) URL() string {
	return d.ConnectorURL
}

func (d *directConnect) Username() string {
	return d.ConnectorUsername
}

func (d *directConnect) Password() redactor.String {
	return d.ConnectorPassword
}

func (d *directConnect) SetPassword(password redactor.String) {
	d.ConnectorPassword = password
}

func (d *directConnect) Config() Config {
	return d.ConnectorConfig
}

// Statement downloads and returns transactions from a connector for the given time period
func Statement(connector Connector, start, end time.Time, requestors []Requestor) ([]ledger.Transaction, error) {
	client, err := newSimpleClient(connector.URL(), connector.Config())
	if err != nil {
		return nil, err
	}

	return fetchTransactions(
		connector,
		start, end,
		requestors,
		// TODO it seems the ledger balance is nearly always the current balance, rather than the statement close. Restore this when a true closing balance can be found
		//balanceTransactions,
		client.Request,
		importTransactions,
	)
}

func fetchTransactions(
	connector Connector,
	start, end time.Time,
	requestors []Requestor,
	doRequest func(*ofxgo.Request) (*ofxgo.Response, error),
	importTransactions func(*ofxgo.Response, transactionParser) ([]model.Account, []ledger.Transaction, error),
) ([]ledger.Transaction, error) {
	var query ofxgo.Request
	for _, r := range requestors {
		if err := r.Statement(&query, start, end); err != nil {
			return nil, err
		}
	}
	if len(query.Bank) == 0 && len(query.CreditCard) == 0 {
		return nil, errors.Errorf("Invalid statement query: does not contain any statement requests: %+v", query)
	}

	config := connector.Config()

	query.URL = connector.URL()
	query.Signon = ofxgo.SignonRequest{
		ClientUID: ofxgo.UID(config.ClientID),
		Org:       ofxgo.String(connector.Org()),
		Fid:       ofxgo.String(connector.FID()),
		UserID:    ofxgo.String(connector.Username()),
		UserPass:  ofxgo.String(connector.Password()),
	}

	response, err := doRequest(&query)
	if err != nil {
		return nil, err
	}

	if response.Signon.Status.Code != 0 {
		if response.Signon.Status.Code == ofxAuthFailed {
			return nil, ErrAuthFailed
		}
		meaning, err := response.Signon.Status.CodeMeaning()
		if err != nil {
			return nil, errors.Wrap(err, "Failed to parse OFX response code")
		}
		return nil, errors.Errorf("Nonzero signon status (%d: %s) with message: %s", response.Signon.Status.Code, meaning, response.Signon.Status.Message)
	}

	_, txns, err := importTransactions(response, parseTransaction)
	return txns, err
}

// Verify attempts to sign in with the given account. Returns any encountered errors
func Verify(connector Connector, requestor Requestor) error {
	end := time.Now()
	start := end.Add(-24 * time.Hour)
	_, err := Statement(connector, start, end, []Requestor{requestor})
	return err
}

// decToPtr makes a copy of d and returns a reference to it
func decToPtr(d decimal.Decimal) *decimal.Decimal {
	return &d
}

func normalizeCurrency(currency string) string {
	switch currency {
	case "USD":
		return "$"
	default:
		return currency
	}
}

type transactionParser func(txn ofxgo.Transaction, currency, accountName string, makeTxnID func(string) string) ledger.Transaction

func parseTransaction(txn ofxgo.Transaction, currency, accountName string, makeTxnID func(string) string) ledger.Transaction {
	if txn.Currency != nil {
		if ok, _ := txn.Currency.Valid(); ok {
			currency = normalizeCurrency(txn.Currency.CurSym.String())
		}
	}

	name := string(txn.Name)
	if name == "" && txn.Payee != nil {
		name = string(txn.Payee.Name)
	}

	// TODO can ofxgo lib support a decimal type instead of big.Rat?
	// NOTE: TrnAmt uses big.Rat internally, which can't form an invalid number with .String()
	amount := decimal.RequireFromString(txn.TrnAmt.String())

	id := makeTxnID(string(txn.FiTID))

	return ledger.Transaction{
		Date:  txn.DtPosted.Time,
		Payee: name,
		Postings: []ledger.Posting{
			{
				Account:  accountName,
				Amount:   amount,
				Balance:  nil, // set balance in next section
				Currency: currency,
				Tags:     map[string]string{"id": id},
			},
			{
				Account:  model.Uncategorized,
				Amount:   amount.Neg(),
				Currency: currency,
			},
		},
	}
}

// balanceTransactions sorts and adds balances to each transaction
func balanceTransactions(txns []ledger.Transaction, balance decimal.Decimal, balanceDate time.Time, statementEndDate time.Time) {
	ledger.Transactions(txns).Sort()

	if balanceDate.After(statementEndDate) {
		// don't trust this balance, it was recorded after the statement end date
		return
	}

	balanceDateIndex := len(txns)
	for i, txn := range txns {
		if txn.Date.After(balanceDate) {
			// the end of balance date
			balanceDateIndex = i
			break
		}
	}

	runningBalance := balance
	for i := balanceDateIndex - 1; i >= 0; i-- {
		txns[i].Postings[0].Balance = decToPtr(runningBalance)
		runningBalance = runningBalance.Sub(txns[i].Postings[0].Amount)
	}
	runningBalance = balance
	for i := balanceDateIndex; i < len(txns); i++ {
		runningBalance = runningBalance.Add(txns[i].Postings[0].Amount)
		txns[i].Postings[0].Balance = decToPtr(runningBalance)
	}
}

func makeUniqueAccountTxnID(account Account) func(string) string {
	return makeUniqueTxnID(account.Institution().FID(), account.ID())
}

func makeUniqueTxnID(fid, accountID string) func(txnID string) string {
	// Follows FITID recommendation from OFX 102 Section 3.2.1
	idPrefix := fid + "-" + accountID + "-"
	return func(txnID string) string {
		id := idPrefix + txnID
		// clean ID for use as an hledger tag
		// TODO move tag (de)serialization into ledger package
		id = strings.Replace(id, ",", "", -1)
		id = strings.Replace(id, ":", "", -1)
		return id
	}
}
