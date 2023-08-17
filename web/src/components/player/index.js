import React from 'react'
import { parseLinkHeader } from '@web3-storage/parse-link-header'
import { useLocation, useParams } from 'react-router-dom'
import useRoom from '../../room';
import { useAccount } from '../../account';

function PlayerPage() {
  const { roomId } = useParams()
  const room = useRoom(roomId)
  return (
    <div className={`flex flex-col items-center mx-auto px-2 py-2 container`}>
      <Streams session={room.session} streamers={room.users.filter((user) => user.streaming)} />
      <OnlineUsers users={room.users} />
    </div>
  )
}

function Streams({ session, streamers }) {
  const account = useAccount()
  return (
    <>
      {streamers.map((streamer) => ( // todo: sort
        <React.Fragment key={streamer.id}>
          <p className='text-lg'>{streamer.id} user video:</p>
          <Player key={streamer.id} account={account} session={session} streamer={streamer} />
        </React.Fragment>
      ))}
    </>
  )
}

function Player({ account, session, streamer, cinemaMode }) {
  const videoRef = React.createRef()
  const location = useLocation()
  const [videoLayers, setVideoLayers] = React.useState([]);
  const [mediaSrcObject, setMediaSrcObject] = React.useState(null);
  const [layerEndpoint, setLayerEndpoint] = React.useState('');

  const onLayerChange = event => {
    fetch(layerEndpoint, {
      method: 'POST',
      body: JSON.stringify({ mediaId: '1', encodingId: event.target.value }),
      headers: {
        'Content-Type': 'application/json'
      }
    })
  }

  React.useEffect(() => {
    if (videoRef.current) {
      videoRef.current.srcObject = mediaSrcObject
    }
  }, [mediaSrcObject])

  React.useEffect(() => {
    const peerConnection = new RTCPeerConnection() // eslint-disable-line

    peerConnection.ontrack = function (event) {
      setMediaSrcObject(event.streams[0])
    }

    peerConnection.addTransceiver('audio', { direction: 'recvonly' })
    peerConnection.addTransceiver('video', { direction: 'recvonly' })

    peerConnection.createOffer().then(offer => {
      peerConnection.setLocalDescription(offer)

      fetch(`${process.env.REACT_APP_API_PATH}/whep/${encodeURIComponent(streamer.id)}?session=${encodeURIComponent(session.id)}`, {
        method: 'POST',
        body: offer.sdp,
        headers: {
          Authorization: `Bearer ` + account.token,
          'Content-Type': 'application/sdp'
        }
      }).then(r => {
        if (!r.ok) {
          throw Error('invalid response status: ' + r.status)
        }
        const parsedLinkHeader = parseLinkHeader(r.headers.get('Link'))
        setLayerEndpoint(`${window.location.protocol}//${parsedLinkHeader['urn:ietf:params:whep:ext:core:layer'].url}`)

        const evtSource = new EventSource(`${window.location.protocol}//${parsedLinkHeader['urn:ietf:params:whep:ext:core:server-sent-events'].url}`)
        evtSource.onerror = err => evtSource.close();

        evtSource.addEventListener("layers", event => {
          const parsed = JSON.parse(event.data)
          setVideoLayers(parsed['1']['layers'].map(l => l.encodingId))
        })
        // todo: close evtSource

        return r.text()
      }).then(answer => {
        peerConnection.setRemoteDescription({
          sdp: answer,
          type: 'answer'
        })
      })
    })

    return function cleanup() {
      peerConnection.close()
    }
  }, [location.pathname, account.token, session.id, streamer.id])

  return (
    <>
      <video
        ref={videoRef}
        autoPlay
        muted
        controls
        playsInline
        className={`bg-black w-full ${cinemaMode && "min-h-screen"}`}
      />

      {videoLayers.length >= 2 &&
        <select defaultValue="disabled" onChange={onLayerChange} className="appearance-none border w-full py-2 px-3 leading-tight focus:outline-none focus:shadow-outline bg-gray-700 border-gray-700 text-white rounded shadow-md placeholder-gray-200">
          <option value="disabled" disabled={true}>Choose Quality Level</option>
          {videoLayers.map(layer => {
            return <option key={layer} value={layer}>{layer}</option>
          })}
        </select>
      }
    </>
  )
}

function OnlineUsers({ users }) {
  return (
    <>
      <p className="text-xl mt-5">Users in room: {users.length}</p>

      {users.map((user) => (
        <User key={user.id} user={user} />
      ))}
    </>
  )
}

function User({ user }) {
  return (
    <>
      <h2>User: {user.id}</h2>
    </>
  )
}

export default PlayerPage
