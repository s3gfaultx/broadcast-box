package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

type (
	whepSession struct {
		videoTrack     *trackMultiCodec
		currentLayer   atomic.Value
		sequenceNumber uint16
		timestamp      uint32
		peerConn       *webrtc.PeerConnection
	}
	simulcastLayerResponse struct {
		EncodingId string `json:"encodingId"`
	}
)

func WHEPLayers(whepSessionId string) ([]byte, error) {
	// streamMapLock.Lock()
	// defer streamMapLock.Unlock()

	layers := []simulcastLayerResponse{}
	// for streamKey := range streamMap {
	// 	streamMap[streamKey].whepSessionsLock.Lock()
	// 	defer streamMap[streamKey].whepSessionsLock.Unlock()

	// 	if _, ok := streamMap[streamKey].whepSessions[whepSessionId]; ok {
	// 		for i := range streamMap[streamKey].videoTrackLabels {
	// 			layers = append(layers, simulcastLayerResponse{EncodingId: streamMap[streamKey].videoTrackLabels[i]})
	// 		}

	// 		break
	// 	}
	// }

	resp := map[string]map[string][]simulcastLayerResponse{
		"1": {
			"layers": layers,
		},
	}

	return json.Marshal(resp)
}

func WHEPChangeLayer(whepSessionId, layer string) error {
	// streamMapLock.Lock()
	// defer streamMapLock.Unlock()

	// for streamKey := range streamMap {
	// 	streamMap[streamKey].whepSessionsLock.Lock()
	// 	defer streamMap[streamKey].whepSessionsLock.Unlock()

	// 	if _, ok := streamMap[streamKey].whepSessions[whepSessionId]; ok {
	// 		streamMap[streamKey].whepSessions[whepSessionId].currentLayer.Store(layer)
	// 		streamMap[streamKey].pliChan <- true
	// 	}
	// }

	return nil
}

func WHEP(offer, authToken string, streamerId uuid.UUID) (string, string, error) {
	roomMapLock.Lock()
	var room *Room
	var streamer *User
	for _, activeRoom := range roomMap {
		if user, ok := activeRoom.users[streamerId]; ok {
			room = activeRoom
			streamer = user
			break
		}
	}
	roomMapLock.Unlock()
	if room == nil || streamer == nil {
		return "", "", errors.New("invalid room id")
	}

	room.lock.Lock()
	defer room.lock.Unlock()

	viewer := room.findByToken(authToken)
	if viewer == nil {
		return "", "", errors.New("unauthorized")
	}
	streamVal := streamer.stream.Load()
	if streamVal == nil {
		return "", "", errors.New("user is not streaming")
	}
	stream := streamVal.(*userStream)

	videoTrack := &trackMultiCodec{id: "video", streamID: "pion"}
	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", "", fmt.Errorf("new peer connection: %s", err)
	}

	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateFailed {
			if err := peerConnection.Close(); err != nil {
				log.Printf("Could not close failed %s user connection: %s", authToken, err)
			}
		} else if state == webrtc.ICEConnectionStateClosed {
			stream.removeViewer(viewer)
		}
	})

	if _, err = peerConnection.AddTrack(stream.audioTrack); err != nil {
		return "", "", err
	}

	rtpSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		return "", "", err
	}

	go func() {
		for {
			rtcpPackets, _, rtcpErr := rtpSender.ReadRTCP()
			if rtcpErr != nil {
				return
			}

			for _, r := range rtcpPackets {
				if _, isPLI := r.(*rtcp.PictureLossIndication); isPLI {
					select {
					case stream.pliChan <- true:
					default:
					}
				}
			}
		}
	}()

	if err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
		SDP:  offer,
		Type: webrtc.SDPTypeOffer,
	}); err != nil {
		return "", "", err
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	answer, err := peerConnection.CreateAnswer(nil)

	if err != nil {
		return "", "", err
	} else if err = peerConnection.SetLocalDescription(answer); err != nil {
		return "", "", err
	}

	<-gatherComplete

	stream.lock.Lock()
	defer stream.lock.Unlock()

	whepSession := &whepSession{
		videoTrack: videoTrack,
		timestamp:  50000,
		peerConn:   peerConnection,
	}
	whepSession.currentLayer.Store("")
	stream.viewers[viewer.Id] = whepSession
	return peerConnection.LocalDescription().SDP, viewer.Id.String(), nil
}

func (w *whepSession) sendVideoPacket(rtpPkt *rtp.Packet, layer string, timeDiff uint32, isAV1 bool) error {
	if w.currentLayer.Load() == "" {
		w.currentLayer.Store(layer)
	} else if layer != w.currentLayer.Load() {
		return nil
	}

	w.sequenceNumber += 1
	w.timestamp += timeDiff

	rtpPkt.SequenceNumber = w.sequenceNumber
	rtpPkt.Timestamp = w.timestamp

	if err := w.videoTrack.WriteRTP(rtpPkt, isAV1); err != nil {
		return fmt.Errorf("write packet: %w", err)
	}
	return nil
}
