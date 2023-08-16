import React from 'react'

export const AccountContext = React.createContext(null)

export function AccountProvider({ children }) {
    const [account] = React.useState(() => {
        let token = localStorage.getItem("authToken")
        if (token === null) {
            token = generateAuthToken()
            localStorage.setItem("authToken", token)
        }
        return {
            token,
        }
    })
    return (
        <AccountContext.Provider value={account}>
            {children}
        </AccountContext.Provider>
    )
}

export function useAccount() {
    return React.useContext(AccountContext)
}

function generateAuthToken() {
    const token = new Uint8Array(128)
    crypto.getRandomValues(token)
    return btoa(token)
}
