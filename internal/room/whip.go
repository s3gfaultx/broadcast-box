package room

import (
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

func audioWriter(remoteTrack *webrtc.TrackRemote, audioTrack *webrtc.TrackLocalStaticRTP) error {
	rtpBuf := make([]byte, 1500)
	for {
		rtpRead, _, err := remoteTrack.Read(rtpBuf)
		if err != nil {
			return fmt.Errorf("read remote track: %w", err)
		}
		if _, err := audioTrack.Write(rtpBuf[:rtpRead]); err != nil {
			return fmt.Errorf("write audio track: %w", err)
		}
	}
}

func videoWriter(remoteTrack *webrtc.TrackRemote, stream *userStream, peerConnection *webrtc.PeerConnection) error {
	id := remoteTrack.RID()
	if id == "" {
		id = videoTrackLabelDefault
	}

	if err := stream.addVideoTrack(id); err != nil {
		return fmt.Errorf("add video track: %w", err)
	}

	go func() {
		for range stream.pliChan {
			if sendErr := peerConnection.WriteRTCP([]rtcp.Packet{
				&rtcp.PictureLossIndication{
					MediaSSRC: uint32(remoteTrack.SSRC()),
				},
			}); sendErr != nil {
				return
			}
		}
	}()

	isAV1 :=
		strings.Contains(
			strings.ToLower(webrtc.MimeTypeAV1),
			strings.ToLower(remoteTrack.Codec().RTPCodecCapability.MimeType),
		)

	rtpBuf := make([]byte, 1500)
	rtpPkt := &rtp.Packet{}
	lastTimestamp := uint32(0)
	for {
		rtpRead, _, err := remoteTrack.Read(rtpBuf)
		if err != nil {
			return fmt.Errorf("read remote track: %w", err)
		}
		if err = rtpPkt.Unmarshal(rtpBuf[:rtpRead]); err != nil {
			return fmt.Errorf("unmarshal rtp packet: %w", err)
		}

		timeDiff := rtpPkt.Timestamp - lastTimestamp
		if lastTimestamp == 0 {
			timeDiff = 0
		}
		lastTimestamp = rtpPkt.Timestamp

		disconnectedViewers := make([]uuid.UUID, 0)
		stream.lock.RLock()
		for viewerId, viewer := range stream.viewers {
			err := viewer.sendVideoPacket(rtpPkt, id, timeDiff, isAV1)
			if err != nil {
				log.Printf("Could not send video packet to %s viewer: %s\n", viewerId, err)
				disconnectedViewers = append(disconnectedViewers, viewerId)
			}
		}
		stream.lock.RUnlock()

		if len(disconnectedViewers) > 0 {
			stream.lock.Lock()
			for _, disconnected := range disconnectedViewers {
				delete(stream.viewers, disconnected)
			}
			stream.lock.Unlock()
		}
	}
}

func FinishWHIP(authToken string) error {
	roomMapLock.Lock()
	defer roomMapLock.Unlock()
	room, user := findUserByAuth(authToken)
	if room == nil {
		return errors.New("not connected to any room")
	}
	room.lock.Lock()
	room.stopStream(user)
	room.lock.Unlock()
	return nil
}

func WHIP(offer, authToken string) (string, error) {
	roomMapLock.Lock()
	defer roomMapLock.Unlock()
	room, user := findUserByAuth(authToken)
	if room == nil {
		return "", errors.New("not connected to any room")
	}
	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", fmt.Errorf("new peer connection: %w", err)
	}
	stream, err := room.startStream(user, peerConnection)
	if err != nil {
		return "", fmt.Errorf("start stream: %w", err)
	}
	log.Printf("Initializing %s user stream in room %s.\n", user.Id, room.id)

	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, rtpReceiver *webrtc.RTPReceiver) {
		mimeType := remoteTrack.Codec().RTPCodecCapability.MimeType
		var err error
		if strings.HasPrefix(mimeType, "audio") {
			err = audioWriter(remoteTrack, stream.audioTrack)
		} else {
			err = videoWriter(remoteTrack, stream, peerConnection)
		}
		switch {
		case errors.Is(err, io.EOF):
			return
		case err != nil:
			log.Printf("Could not handle %s user '%s' track: %s\n", user.Id, mimeType, err)
			return
		}
	})

	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("ICE connection state change of user %s: %s\n", user.Id, state)
		if state == webrtc.ICEConnectionStateFailed {
			if err := peerConnection.Close(); err != nil {
				log.Printf("Could not close failed peer connection of user %s: %s\n", user.Id, err)
			}
		} else if state == webrtc.ICEConnectionStateClosed {
			room.lock.Lock()
			room.stopStream(user)
			room.lock.Unlock()
		}
	})

	if err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
		SDP:  string(offer),
		Type: webrtc.SDPTypeOffer,
	}); err != nil {
		return "", fmt.Errorf("set remote description: %s", err)
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %s", err)
	} else if err = peerConnection.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set local description: %s", err)
	}

	<-gatherComplete
	return peerConnection.LocalDescription().SDP, nil
}

// func GetAllStreams() (out []string) {
// 	streamMapLock.Lock()
// 	defer streamMapLock.Unlock()

// 	for s := range streamMap {
// 		out = append(out, s)
// 	}

// 	return
// }
