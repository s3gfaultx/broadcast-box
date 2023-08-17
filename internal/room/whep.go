package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync/atomic"

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

func WHEPLayers(user, streamer *User) ([]byte, error) {
	roomMapLock.Lock()
	defer roomMapLock.Unlock()

	stream := streamer.stream.Load().(*userStream)
	if stream == nil {
		return nil, errors.New("streamer is not streaming")
	}
	layers := []simulcastLayerResponse{}
	for _, label := range stream.videoTrackLabels {
		layers = append(layers, simulcastLayerResponse{EncodingId: label})
	}
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

func WHEP(offer string, viewerSessionId SessionId, streamerId UserId) (string, error) {
	room, viewerSession := FindSession(viewerSessionId)
	if room == nil || viewerSession == nil {
		return "", errors.New("viewer session not found")
	}

	room.lock.Lock()
	defer room.lock.Unlock()

	streamer := room.user(streamerId)
	if streamer == nil {
		return "", errors.New("streamer not found")
	}
	streamVal := streamer.stream.Load()
	if streamVal == nil {
		return "", errors.New("user is not streaming")
	}
	stream := streamVal.(*userStream)

	videoTrack := &trackMultiCodec{id: "video", streamID: "pion"}
	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", fmt.Errorf("new peer connection: %s", err)
	}

	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateFailed {
			if err := peerConnection.Close(); err != nil {
				log.Printf("Could not close failed %s user WHEP connection: %s",
					viewerSessionId.String(), err)
			}
		} else if state == webrtc.ICEConnectionStateClosed {
			stream.removeViewer(viewerSession)
		}
	})

	if _, err = peerConnection.AddTrack(stream.audioTrack); err != nil {
		return "", err
	}

	rtpSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		return "", err
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
		return "", err
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	answer, err := peerConnection.CreateAnswer(nil)

	if err != nil {
		return "", err
	} else if err = peerConnection.SetLocalDescription(answer); err != nil {
		return "", err
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
	stream.viewers[viewerSession.Id] = whepSession
	return peerConnection.LocalDescription().SDP, nil
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

func (w *whepSession) close() {
	_ = w.peerConn.Close()
}
