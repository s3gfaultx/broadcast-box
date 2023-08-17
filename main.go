package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"strings"

	"log"
	"net/http"

	"github.com/glimesh/broadcast-box/internal/room"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

const (
	envFileProd = ".env.production"
	envFileDev  = ".env.development"
)

type (
	whepLayerRequestJSON struct {
		MediaId    string `json:"mediaId"`
		EncodingId string `json:"encodingId"`
	}
)

func logHTTPError(w http.ResponseWriter, err string, code int) {
	log.Println(err)
	http.Error(w, err, code)
}

func whipHandler(res http.ResponseWriter, r *http.Request) {
	streamKey := r.Header.Get("Authorization")
	streamKey = strings.TrimPrefix(streamKey, "Bearer ")
	if streamKey == "" {
		logHTTPError(res, "Authorization was not set", http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodDelete {
		if err := room.FinishWHIP(streamKey); err != nil {
			logHTTPError(res, err.Error(), http.StatusBadRequest)
		}
		return
	}

	offer, err := io.ReadAll(r.Body)
	if err != nil {
		logHTTPError(res, err.Error(), http.StatusBadRequest)
		return
	}

	answer, err := room.WHIP(string(offer), streamKey)
	if err != nil {
		logHTTPError(res, err.Error(), http.StatusBadRequest)
		return
	}

	res.Header().Add("Location", "/api/whip")
	res.WriteHeader(http.StatusCreated)
	fmt.Fprint(res, answer)
}

func whepHandler(res http.ResponseWriter, req *http.Request) {
	vals := strings.Split(req.URL.Path, "/")
	streamerIdStr := vals[len(vals)-1]
	log.Println("Wheep handler of streamer id", streamerIdStr)
	streamerId, err := uuid.Parse(streamerIdStr)
	if err != nil {
		logHTTPError(res, fmt.Errorf("parse streamer id: %w", err).Error(), http.StatusBadRequest)
		return
	}

	authToken := req.Header.Get("Authorization")
	authToken = strings.TrimPrefix(authToken, "Bearer ")
	if authToken == "" {
		logHTTPError(res, "Authorization was not set", http.StatusBadRequest)
		return
	}

	offer, err := io.ReadAll(req.Body)
	if err != nil {
		logHTTPError(res, err.Error(), http.StatusBadRequest)
		return
	}

	answer, streamerIdStr, err := room.WHEP(string(offer), authToken, streamerId)
	if err != nil {
		logHTTPError(res, err.Error(), http.StatusBadRequest)
		return
	}

	apiPath := req.Host + strings.TrimSuffix(req.URL.RequestURI(), "whep")
	res.Header().Add("Link", `<`+apiPath+"sse/"+streamerIdStr+`>; rel="urn:ietf:params:whep:ext:core:server-sent-events"; events="layers"`)
	res.Header().Add("Link", `<`+apiPath+"layer/"+streamerIdStr+`>; rel="urn:ietf:params:whep:ext:core:layer"`)
	fmt.Fprint(res, answer)
}

func whepServerSentEventsHandler(res http.ResponseWriter, req *http.Request) {
	res.Header().Set("Content-Type", "text/event-stream")
	res.Header().Set("Cache-Control", "no-cache")
	res.Header().Set("Connection", "keep-alive")

	vals := strings.Split(req.URL.RequestURI(), "/")
	whepSessionId := vals[len(vals)-1]

	layers, err := room.WHEPLayers(whepSessionId)
	if err != nil {
		logHTTPError(res, err.Error(), http.StatusBadRequest)
		return
	}

	fmt.Fprint(res, "event: layers\n")
	fmt.Fprintf(res, "data: %s\n", string(layers))
	fmt.Fprint(res, "\n\n")
}

func whepLayerHandler(res http.ResponseWriter, req *http.Request) {
	var r whepLayerRequestJSON
	if err := json.NewDecoder(req.Body).Decode(&r); err != nil {
		logHTTPError(res, err.Error(), http.StatusBadRequest)
		return
	}

	vals := strings.Split(req.URL.RequestURI(), "/")
	whepSessionId := vals[len(vals)-1]

	if err := room.WHEPChangeLayer(whepSessionId, r.EncodingId); err != nil {
		logHTTPError(res, err.Error(), http.StatusBadRequest)
		return
	}
}

func roomEvents(res http.ResponseWriter, req *http.Request) {
	vals := strings.Split(req.URL.Path, "/")
	roomId := vals[len(vals)-1]

	authToken := req.URL.Query().Get("authToken")
	if authToken == "" {
		logHTTPError(res, "authToken query was not set", http.StatusUnauthorized)
		return
	}

	flusher, ok := res.(http.Flusher)
	if !ok {
		logHTTPError(res, "streaming unsupported", http.StatusBadRequest)
		return
	}

	room, user, err := room.Join(roomId, authToken)
	if err != nil {
		logHTTPError(res, err.Error(), http.StatusBadRequest)
		return
	}
	defer func() {
		room.RemoveUser(user)
	}()

	res.Header().Set("Content-Type", "text/event-stream")
	res.Header().Set("Cache-Control", "no-cache")
	res.Header().Set("Connection", "keep-alive")

	for {
		select {
		case event, ok := <-user.Events:
			if !ok {
				return
			}
			serialized, err := json.Marshal(event)
			if err != nil {
				logHTTPError(res, fmt.Errorf("marshal event: %s", err.Error()).Error(), http.StatusInternalServerError)
				return
			}
			_, err = fmt.Fprintf(res, "event: %s\ndata: %s\n\n", event.Type(), serialized)
			if err != nil {
				logHTTPError(res, fmt.Errorf("write event: %s", err.Error()).Error(), http.StatusInternalServerError)
				return
			}
			flusher.Flush()
		case <-req.Context().Done():
			return
		}
	}
}

type StreamStatus struct {
	StreamKey string `json:"streamKey"`
}

func statusHandler(res http.ResponseWriter, req *http.Request) {
	// statuses := []StreamStatus{}
	// for _, s := range webrtc.GetAllStreams() {
	// 	statuses = append(statuses, StreamStatus{StreamKey: s})
	// }

	// if err := json.NewEncoder(res).Encode(statuses); err != nil {
	// 	logHTTPError(res, err.Error(), http.StatusBadRequest)
	// }
}

func indexHTMLWhenNotFound(fs http.FileSystem) http.Handler {
	fileServer := http.FileServer(fs)

	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		_, err := fs.Open(path.Clean(req.URL.Path)) // Do not allow path traversals.
		if errors.Is(err, os.ErrNotExist) {
			http.ServeFile(resp, req, "./web/build/index.html")

			return
		}
		fileServer.ServeHTTP(resp, req)
	})
}

func corsHandler(next func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Access-Control-Allow-Origin", "*")
		res.Header().Set("Access-Control-Allow-Methods", "*")
		res.Header().Set("Access-Control-Allow-Headers", "*")
		res.Header().Set("Access-Control-Expose-Headers", "*")

		if req.Method != http.MethodOptions {
			next(res, req)
		}
	}
}

func main() {
	if os.Getenv("APP_ENV") == "production" {
		log.Println("Loading `" + envFileProd + "`")

		if err := godotenv.Load(envFileProd); err != nil {
			log.Fatal(err)
		}
	} else {
		log.Println("Loading `" + envFileDev + "`")

		if err := godotenv.Load(envFileDev); err != nil {
			log.Fatal(err)
		}
	}

	room.Configure()

	mux := http.NewServeMux()
	mux.Handle("/", indexHTMLWhenNotFound(http.Dir("./web/build")))
	mux.HandleFunc("/api/whip", corsHandler(whipHandler))
	mux.HandleFunc("/api/whep/", corsHandler(whepHandler))
	mux.HandleFunc("/api/room/", corsHandler(roomEvents))
	mux.HandleFunc("/api/status", corsHandler(statusHandler))
	mux.HandleFunc("/api/sse/", corsHandler(whepServerSentEventsHandler))
	mux.HandleFunc("/api/layer/", corsHandler(whepLayerHandler))

	log.Println("Running HTTP Server at `" + os.Getenv("HTTP_ADDRESS") + "`")

	srv := &http.Server{
		Handler: mux,
		Addr:    os.Getenv("HTTP_ADDRESS"),
	}
	go func() {
		log.Fatalln(srv.ListenAndServe())
	}()

	interruptChan := make(chan os.Signal, 1)
	signal.Notify(interruptChan, os.Interrupt)
	<-interruptChan

	log.Println("Shutting down...")
	room.CloseAll()
}
