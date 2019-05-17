package ledger

import (
	"bufio"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	idTag      = "id"
	dateFormat = "2006/01/02"
)

type Transaction struct {
	Comment  string
	Date     time.Time
	Payee    string
	Postings []Posting
	Tags     map[string]string
}

func readAllTransactions(scanner *bufio.Scanner) ([]Transaction, error) {
	var transactions []Transaction
	var txn Transaction
	readingPostings := false

	endTxn := func() error {
		if !readingPostings {
			return nil
		}
		if len(txn.Postings) < 2 {
			return fmt.Errorf("A transaction must have at least two postings:\n%s", txn.String())
		}
		var sum decimal.Decimal
		for _, p := range txn.Postings[:len(txn.Postings)-1] {
			if p.Amount == nil {
				return fmt.Errorf("A transaction's postings may only have one missing amount, and it must be the last posting:\n%s", txn.String())
			}
			sum = sum.Sub(*p.Amount)
		}
		lastPosting := &txn.Postings[len(txn.Postings)-1]
		if lastPosting.Amount == nil {
			lastPosting.Amount = &sum
			lastPosting.Currency = usd
		} else if !lastPosting.Amount.Equal(sum) {
			return fmt.Errorf("Detected unbalanced transaction:\n%s", txn.String())
		}
		// valid txn
		readingPostings = false
		transactions = append(transactions, txn)
		txn = Transaction{}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if trimLine := strings.TrimSpace(line); trimLine == "" || trimLine[0] == ';' {
			// is blank line
			if err := endTxn(); err != nil {
				return nil, err
			}
		} else if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if err := endTxn(); err != nil {
				return nil, err
			}
			// is txn payee line
			err := parsePayeeLine(&txn, line)
			if err != nil {
				return nil, err
			}
			readingPostings = true
		} else if readingPostings {
			// is posting line
			posting, err := NewPostingFromString(line)
			if err != nil {
				return nil, err
			}
			txn.Postings = append(txn.Postings, posting)
		} else {
			return nil, fmt.Errorf("Unknown line format detected: %s", line)
		}
	}
	if err := endTxn(); err != nil {
		return nil, err
	}
	return transactions, nil
}

func parsePayeeLine(txn *Transaction, line string) error {
	tokens := strings.SplitN(line, ";", 2)
	line = strings.TrimSpace(tokens[0])
	if len(tokens) == 2 {
		txn.Comment, txn.Tags = parseTags(strings.TrimSpace(tokens[1]))
	}
	tokens = strings.SplitN(line, " ", 2)
	if len(tokens) != 2 {
		return fmt.Errorf("Not enough tokens for payee line: %s", line)
	}
	date, payee := strings.TrimSpace(tokens[0]), strings.TrimSpace(tokens[1])
	txn.Payee = payee
	var err error
	txn.Date, err = time.Parse(dateFormat, date)
	if err != nil {
		return err
	}
	return nil
}

func parseTags(comment string) (string, map[string]string) {
	if !strings.ContainsRune(comment, ':') {
		return comment, nil
	}

	tags := make(map[string]string)
	commentEnd := strings.LastIndexByte(comment[:strings.IndexRune(comment, ':')], ' ')
	var newComment string
	if commentEnd != -1 {
		newComment = strings.TrimSpace(comment[:commentEnd])
	}
	tagStrings := strings.Split(comment[commentEnd+1:], ",")
	for _, tagString := range tagStrings {
		keyValue := strings.SplitN(tagString, ":", 2)
		if len(keyValue) != 2 {
			return comment, nil
		}
		key, value := strings.TrimSpace(keyValue[0]), strings.TrimSpace(keyValue[1])
		tags[key] = value
	}
	return newComment, tags
}

func serializeComment(comment string, tags map[string]string) string {
	if len(tags) > 0 {
		tagStrings := make([]string, 0, len(tags))
		for k, v := range tags {
			tagStrings = append(tagStrings, fmt.Sprintf("%s: %s", k, v))
		}
		sort.Strings(tagStrings)
		if comment != "" {
			comment += " "
		}
		comment += strings.Join(tagStrings, ", ")
	}
	if comment != "" {
		comment = " ; " + comment
	}
	return comment
}

func max(a, b int) int {
	if a < b {
		return b
	}
	return a
}

func (t Transaction) ID() string {
	return t.Tags[idTag]
}

func (t Transaction) String() string {
	postings := make([]string, 0, len(t.Postings))
	accountLen, amountLen := 0, 0
	for _, posting := range t.Postings {
		accountLen = max(accountLen, len(posting.Account))
		if posting.Amount != nil {
			amountLen = max(amountLen, len(posting.Amount.String()))
		}
	}
	for _, posting := range t.Postings {
		postings = append(postings, posting.FormatTable(-accountLen, amountLen))
	}
	return fmt.Sprintf(
		"%4d/%02d/%02d %s%s\n    %s\n",
		t.Date.Year(),
		t.Date.Month(),
		t.Date.Day(),
		t.Payee,
		serializeComment(t.Comment, t.Tags),
		strings.Join(postings, "\n    "),
	)
}
