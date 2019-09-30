import React from 'react';
import axios from 'axios';
import Form from 'react-bootstrap/Form';


export function cleanCategory(account) {
  let i = account.lastIndexOf(":")
  if (i === -1) {
    return account
  }
  return account.slice(i + 1)
}

let categoriesPromise = null

export const Categories = () => {
  if (categoriesPromise === null) {
    categoriesPromise = axios.get('/api/v1/getCategories')
      .then(res => res.data.Accounts)
      .then(accounts =>
        accounts.map(c => [c, c.replace(/:/g, ' > ')]))
  }
  return categoriesPromise
}

export function CategoryPicker({ category, setCategory, filter, disabled }) {
  if (! setCategory) {
    throw Error("setCategory is required")
  }
  const [categories, setCategories] = React.useState([])
  React.useEffect(() => {
    Categories().then(categories => {
      if (filter) {
        categories = categories.filter(c => filter(c[0]))
      }
      setCategories(categories)
    })
  }, [filter])
  if (categories.length === 0) {
    return null
  }
  if (category === null) {
    setCategory(categories[0][0])
    return null
  }
  return (
    <Form.Control
      as="select"
      disabled={disabled}
      value={category}
      onChange={e => setCategory(e.target.value)}
      className="category">
      {categories.map(c =>
        <option key={c[0]} value={c[0]}>{c[1]}</option>
      )}
    </Form.Control>
  )
}

export default CategoryPicker;
