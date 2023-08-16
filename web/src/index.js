import React from 'react'
import ReactDOM from 'react-dom/client'
import './index.css'
import App from './App'
import { CinemaModeProvider } from './components/player'
import { AccountProvider } from './account'

const root = ReactDOM.createRoot(document.getElementById('root'))
root.render(
  <React.StrictMode>
    <AccountProvider>
      <CinemaModeProvider>
        <App />
      </CinemaModeProvider>
    </AccountProvider>
  </React.StrictMode>
)
