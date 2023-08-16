import React from 'react'
import { useAccount } from './account'

function useRoom(roomId) {
    const account = useAccount()
    const [room, setRoom] = React.useState({
        id: roomId,
        users: [],
    })

    React.useEffect(() => {
        const handlers = {
            "users": ({ users }) => setRoom((room) => ({ ...room, users, })),
        }

        const events = new EventSource(`${process.env.REACT_APP_API_PATH}/room/${encodeURIComponent(roomId)}?` +
            `authToken=${account.token}`)
        for (const [event, handler] of Object.entries(handlers)) {
            events.addEventListener(event, ({ data }) => handler(JSON.parse(data)))
        }
        return () => {
            events.close()
        }
    }, [account.token, roomId])

    return room
}

export default useRoom
