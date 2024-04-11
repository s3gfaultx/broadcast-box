package webrtc

import (
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/pion/interceptor/pkg/jitterbuffer"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

func audioWriter(remoteTrack *webrtc.TrackRemote, stream *stream) {
	rtpBuf := make([]byte, 1500)
	for {
		rtpRead, _, err := remoteTrack.Read(rtpBuf)
		switch {
		case errors.Is(err, io.EOF):
			return
		case err != nil:
			log.Println(err)
			return
		}

		stream.audioPacketsReceived.Add(1)
		if _, writeErr := stream.audioTrack.Write(rtpBuf[:rtpRead]); writeErr != nil && !errors.Is(writeErr, io.ErrClosedPipe) {
			log.Println(writeErr)
			return
		}
	}
}

func videoWriter(remoteTrack *webrtc.TrackRemote, stream *stream, peerConnection *webrtc.PeerConnection, s *stream) {
	id := remoteTrack.RID()
	if id == "" {
		id = videoTrackLabelDefault
	}

	videoTrack, err := addTrack(s, id)
	if err != nil {
		log.Println(err)
		return
	}

	go func() {
		for {
			select {
			case <-stream.whipActiveContext.Done():
				return
			case <-stream.pliChan:
				if sendErr := peerConnection.WriteRTCP([]rtcp.Packet{
					&rtcp.PictureLossIndication{
						MediaSSRC: uint32(remoteTrack.SSRC()),
					},
				}); sendErr != nil {
					return
				}
			}
		}
	}()

	lastTimestamp := uint32(0)
	codec := getVideoTrackCodec(remoteTrack.Codec().RTPCodecCapability.MimeType)

	jitterBuffer := jitterbuffer.New()
	lastSequenceNumber := uint16(0)

	for {
		recvPkt, _, err := remoteTrack.ReadRTP()
		switch {
		case errors.Is(err, io.EOF):
			return
		case err != nil:
			log.Println(err)
			return
		}

		jitterBuffer.Push(recvPkt)

		for {
			popPkt, err := jitterBuffer.Pop()
			if err != nil || popPkt == nil {
				if !errors.Is(err, jitterbuffer.ErrPopWhileBuffering) {
					timestampDifference := int64(recvPkt.Timestamp) - int64(lastTimestamp)
					if float64(timestampDifference)/float64(remoteTrack.Codec().ClockRate) >= .7 {
						//fmt.Println(timestampDifference, recvPkt.Timestamp, lastTimestamp)
						jitterBuffer.SetPlayoutHead(jitterBuffer.PlayoutHead() + 1)
					}
				}
				break
			}

			if lastSequenceNumber+1 != popPkt.SequenceNumber && false {
				fmt.Println(lastSequenceNumber, popPkt.SequenceNumber)
			}

			videoTrack.packetsReceived.Add(1)

			popPkt.Extension = false
			popPkt.Extensions = nil

			timeDiff := popPkt.Timestamp - lastTimestamp
			if lastTimestamp == 0 {
				timeDiff = 0
			}
			if timeDiff != 0 {
				fmt.Println(timeDiff)
			}
			lastTimestamp = popPkt.Timestamp
			lastSequenceNumber = popPkt.SequenceNumber

			s.whepSessionsLock.RLock()
			for i := range s.whepSessions {
				s.whepSessions[i].sendVideoPacket(popPkt, id, timeDiff, codec)
			}
			s.whepSessionsLock.RUnlock()
		}
	}
}

func WHIP(offer, streamKey string) (string, error) {
	peerConnection, err := newPeerConnection(apiWhip)
	if err != nil {
		return "", err
	}

	streamMapLock.Lock()
	defer streamMapLock.Unlock()
	stream, err := getStream(streamKey, true)
	if err != nil {
		return "", err
	}

	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, rtpReceiver *webrtc.RTPReceiver) {
		if strings.HasPrefix(remoteTrack.Codec().RTPCodecCapability.MimeType, "audio") {
			audioWriter(remoteTrack, stream)
		} else {
			videoWriter(remoteTrack, stream, peerConnection, stream)

		}
	})

	peerConnection.OnICEConnectionStateChange(func(i webrtc.ICEConnectionState) {
		if i == webrtc.ICEConnectionStateFailed || i == webrtc.ICEConnectionStateClosed {
			if err := peerConnection.Close(); err != nil {
				log.Println(err)
			}
			peerConnectionDisconnected(streamKey, "")
		}
	})

	if err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
		SDP:  string(offer),
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
	return peerConnection.LocalDescription().SDP, nil
}
