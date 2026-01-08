//
// Go Asterisk Gateway (GoAstGateway:GAG)
//

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

// --- 定数・構造体定義 ---

const (
	StateIdle    = 0
	StateRinging = 1
	StateTalking = 2
)

type Config struct {
	AsteriskAddr       string   `json:"asterisk_addr"`
	BrowserAddr        string   `json:"browser_addr"`
	CertFile           string   `json:"cert_file"`
	KeyFile            string   `json:"key_file"`
	AsteriskFormat     string   `json:"asterisk_format"`
	ExtensionVariable  string   `json:"extension_variable"`
        ExtenSearchPattern string   `json:"exten_search_pattern"`
	JWTSecret          string   `json:"jwt_secret"`
	AllowedOrigins     []string `json:"allowed_origins"`
	AllowedAsteriskIPs []string `json:"allowed_asterisk_ips"`
	AllowedBrowserIPs  []string `json:"allowed_browser_ips"`
	LogLevel           string   `json:"log_level"` // DEBUG, INFO, WARN, ERROR
}

type GroupDef struct {
	Strategy string   `json:"strategy"`
	Members  []string `json:"members"`
	Timeout  int      `json:"timeout"`
}

type AsteriskJSONMessage struct {
	Event            string            `json:"event"`
	ConnectionID     string            `json:"connection_id"`
	ChannelVariables map[string]string `json:"channel_variables"`
}

type PhoneClaims struct {
	Extension string `json:"ext"`
	jwt.RegisteredClaims
}

type ClientSession struct {
	Extension        string
	Conn             *websocket.Conn
	AudioFromBrowser chan []byte
	ControlChan      chan string
	State            int
	mu               sync.Mutex
}

// --- グローバル変数 ---

var appConfig Config
var groupConfig map[string]GroupDef

var browserUpgrader = websocket.Upgrader{
	EnableCompression: false,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		for _, allowed := range appConfig.AllowedOrigins {
			if origin == allowed {
				return true
			}
		}
		slog.Warn("Blocked Origin", "origin", origin)
		return false
	},
}

var asteriskUpgrader = websocket.Upgrader{
	EnableCompression: false,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type ClientRegistry struct {
	sessions map[string]*ClientSession
	mu       sync.RWMutex
}

var registry = ClientRegistry{
	sessions: make(map[string]*ClientSession),
}

// --- メソッド実装 ---

func (s *ClientSession) TrySetState(newState int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.State != StateIdle && s.State != StateRinging {
		return false
	}
	if s.State == StateRinging && newState == StateRinging {
		return false
	}
	s.State = newState
	return true
}

func (s *ClientSession) ResetState() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.State = StateIdle
}

func (s *ClientSession) GetState() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.State
}

func checkIP(remoteAddr string, allowedList []string) bool {
	if len(allowedList) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	targetIP := net.ParseIP(host)
	if targetIP == nil {
		return false
	}
	for _, allowed := range allowedList {
		if strings.Contains(allowed, "/") {
			_, ipNet, err := net.ParseCIDR(allowed)
			if err == nil && ipNet.Contains(targetIP) {
				return true
			}
		} else {
			allowedIP := net.ParseIP(allowed)
			if allowedIP != nil && allowedIP.Equal(targetIP) {
				return true
			}
		}
	}
	return false
}

func (r *ClientRegistry) Register(ext string, conn *websocket.Conn) (*ClientSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

        if _, ok := r.sessions[ext]; ok {
        // 既に接続がある場合は、新しい接続を拒否する
		slog.Warn("Connection refused: Extension already active", "ext", ext)
		return nil, fmt.Errorf("BUSY")
	}

	session := &ClientSession{
		Extension:        ext,
		Conn:             conn,
		AudioFromBrowser: make(chan []byte, 100),
		ControlChan:      make(chan string),
		State:            StateIdle,
	}

	r.sessions[ext] = session
	slog.Info("Extension Register", "ext", ext, "total", len(r.sessions))
	return session, nil
}

func (r *ClientRegistry) Unregister(ext string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[ext]; ok {
		delete(r.sessions, ext)
		slog.Info("Extension Unregister", "ext", ext)
	}
}

func (r *ClientRegistry) Get(ext string) (*ClientSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.sessions[ext]
	return session, ok
}

// --- グループ着信ロジック ---

func processRingAll(groupID string, conf GroupDef) (*ClientSession, error) {
	slog.Debug("RingAll Start", "GroupID", groupID, "members", conf.Members)
	
	var ringingSessions []*ClientSession
	for _, ext := range conf.Members {
		if sess, ok := registry.Get(ext); ok {
			if sess.TrySetState(StateRinging) {
				ringingSessions = append(ringingSessions, sess)
			} else {
				slog.Debug("Member is BUSY", "ext", ext)
			}
		}
	}

	if len(ringingSessions) == 0 {
		return nil, fmt.Errorf("all members busy or offline")
	}

	winnerChan := make(chan *ClientSession)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, sess := range ringingSessions {
		go func(s *ClientSession) {
			s.mu.Lock()
			s.Conn.WriteMessage(websocket.TextMessage, []byte("RINGING"))
			s.mu.Unlock()

			select {
			case cmd := <-s.ControlChan:
				if cmd == "ANSWER" {
					select {
					case winnerChan <- s:
						return
					case <-ctx.Done():
						s.ResetState()
						s.Conn.WriteMessage(websocket.TextMessage, []byte("HANGUP"))
						return
					}
				} else if cmd == "HANGUP" {
					s.ResetState()
					return
				}
			case <-ctx.Done():
				s.ResetState()
				s.Conn.WriteMessage(websocket.TextMessage, []byte("HANGUP"))
				return
			}
		}(sess)
	}

	select {
	case winner := <-winnerChan:
		slog.Debug("RingAll Winner", "GroupID", groupID, "Winner", winner.Extension)
		return winner, nil
	case <-time.After(time.Duration(conf.Timeout) * time.Second):
		return nil, fmt.Errorf("timeout")
	}
}

func processSequential(groupID string, conf GroupDef) (*ClientSession, error) {
	slog.Debug("Sequential Start", "Group ID", groupID, "members", conf.Members)

	for {
		for _, ext := range conf.Members {
			sess, ok := registry.Get(ext)
			if !ok {
				continue
			}
			if !sess.TrySetState(StateRinging) {
				slog.Debug("Member is BUSY. Next.", "ext", ext)
				continue
			}

			slog.Debug(" -> Ringing", "ext", ext)
			sess.Conn.WriteMessage(websocket.TextMessage, []byte("RINGING"))

			timeout := time.After(time.Duration(conf.Timeout) * time.Second)
			var winner *ClientSession

			select {
			case cmd := <-sess.ControlChan:
				if cmd == "ANSWER" {
					winner = sess
				} else if cmd == "HANGUP" {
					slog.Debug(" -> Rejected. Next.", "ext", ext)
					sess.ResetState()
				}
			case <-timeout:
				slog.Debug(" -> Timeout. Next.", "ext", ext)
				sess.Conn.WriteMessage(websocket.TextMessage, []byte("HANGUP"))
				sess.ResetState()
			}

			if winner != nil {
				return winner, nil
			}
		}
		// ループさせない場合はここで制御
		//return nil, fmt.Errorf("no answer")
	}
}

// --- メイン処理 ---

func normalizeAddress(addr string) string {
	if !strings.Contains(addr, ":") {
		return ":" + addr
	}
	return addr
}

func createAsteriskCommand(cmd string) []byte {
	if appConfig.AsteriskFormat == "json" {
		return []byte(`{"command": "` + cmd + `"}`)
	}
	return []byte(cmd)
}

func loadConfig(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(file).Decode(&appConfig)
}

func loadGroups(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(file).Decode(&groupConfig)
}

func main() {
	// 起動用の初期ロガーを設定 (Infoレベル)
	initialLogger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(initialLogger)

	configPath := flag.String("c", "/usr/local/etc/goastgateway.json", "Config file path")
	groupPath := flag.String("g", "/usr/local/etc/gag_groups.json", "Group definitions path")
	flag.Parse()

	if err := loadConfig(*configPath); err != nil {
		// log.Fatalの代わり
		slog.Error("Config Load Error", "path", *configPath, "err", err)
		os.Exit(1)
	}
	slog.Info("Config loaded", "path", *configPath)

	if err := loadGroups(*groupPath); err != nil {
		slog.Warn("Group Config Load Error (Groups disabled)", "path", *groupPath, "err", err)
		groupConfig = make(map[string]GroupDef)
	} else {
		slog.Info("Groups loaded", "path", *groupPath, "count", len(groupConfig))
	}

	// 設定読み込み後にロガーを再設定
	var logLevel slog.Level
	switch strings.ToUpper(appConfig.LogLevel) {
	case "DEBUG":
		logLevel = slog.LevelDebug
	case "WARN":
		logLevel = slog.LevelWarn
	case "ERROR":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	// AddSource: true にするとログに行番号が出ますが、本番ではfalseが一般的
	opts := &slog.HandlerOptions{
		Level: logLevel,
	}
	// 必要であれば JSONHandler に変更可能
	finalLogger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(finalLogger)

	slog.Info("Logger initialized", "level", logLevel.String())

	appConfig.AsteriskAddr = normalizeAddress(appConfig.AsteriskAddr)
	appConfig.BrowserAddr = normalizeAddress(appConfig.BrowserAddr)
	if appConfig.AsteriskFormat == "" {
		appConfig.AsteriskFormat = "text"
	}

	// Asterisk Server
	http.HandleFunc("/", handleAsteriskConnection)
	go func() {
		slog.Info("Asterisk Server listening", "addr", appConfig.AsteriskAddr)
		if err := http.ListenAndServe(appConfig.AsteriskAddr, nil); err != nil {
			slog.Error("Asterisk Listen Failed", "err", err)
			os.Exit(1)
		}
	}()

	// Browser Server
	browserMux := http.NewServeMux()
	browserMux.HandleFunc("/phone", handlePhoneConnection)
	slog.Info("Browser Server listening (TLS)", "addr", appConfig.BrowserAddr)
	if err := http.ListenAndServeTLS(appConfig.BrowserAddr, appConfig.CertFile, appConfig.KeyFile, browserMux); err != nil {
		slog.Error("Browser Listen Failed", "err", err)
		os.Exit(1)
	}
}

// --- ハンドラ実装 ---

func handlePhoneConnection(w http.ResponseWriter, r *http.Request) {
	if !checkIP(r.RemoteAddr, appConfig.AllowedBrowserIPs) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	tokenString := r.URL.Query().Get("token")
	if tokenString == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	claims := &PhoneClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return []byte(appConfig.JWTSecret), nil
	})
	if err != nil || !token.Valid {
		slog.Warn("JWT Verification Failed", "err", err, "remote", r.RemoteAddr)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ext := claims.Extension
	if ext == "" {
		http.Error(w, "No Extension in Token", http.StatusUnauthorized)
		return
	}

	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("Upgrade error", "err", err)
		return
	}

	slog.Info("Auth Success", "ext", ext, "remote", r.RemoteAddr)

	session, err := registry.Register(ext, conn)
	if err != nil {
		if err.Error() == "BUSY" {
			conn.WriteMessage(websocket.TextMessage, []byte("BUSY"))
			time.Sleep(100 * time.Millisecond)
		}
		conn.Close()
		return
	}
	defer registry.Unregister(ext)
	defer conn.Close()

	for {
		mt, message, err := conn.ReadMessage()
		if err != nil {
			slog.Info("Disconnected", "ext", ext, "err", err)

			// Asterisk側のセッションを開放するためにHANGUPを送る
			go func() {
				// ブロック回避のためselectを使用
				select {
				case session.ControlChan <- "HANGUP":
					slog.Debug("Internal HANGUP sent due to disconnect", "ext", ext)
				case <-time.After(1 * time.Second):
					slog.Debug("Internal HANGUP skipped (channel closed)", "ext", ext)
				}
			}()
			break
		}

		if mt == websocket.TextMessage {
			cmd := string(message)
			select {
			case session.ControlChan <- cmd:
			default:
			}
		}

		if mt == websocket.BinaryMessage {
			if session.GetState() == StateTalking {
				select {
				case session.AudioFromBrowser <- message:
				default:
				}
			}
		}
	}
}

func handleAsteriskConnection(w http.ResponseWriter, r *http.Request) {
	if !checkIP(r.RemoteAddr, appConfig.AllowedAsteriskIPs) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	conn, err := asteriskUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("Asterisk Upgrade error", "err", err)
		return
	}
	defer conn.Close()

	_, msg, err := conn.ReadMessage()
	if err != nil {
		return
	}
	msgStr := string(msg)

	var targetExt string
	var isMediaStart bool

	if appConfig.AsteriskFormat == "json" {
		var jsonMsg AsteriskJSONMessage
		if json.Unmarshal(msg, &jsonMsg) == nil {
			if jsonMsg.Event == "MEDIA_START" {
				isMediaStart = true
			}
			targetExt = jsonMsg.ChannelVariables[appConfig.ExtensionVariable]
			if targetExt == "" {
                                targetPattern := appConfig.ExtenSearchPattern
                                r, err := regexp.Compile(targetPattern)
                                if err != nil {
                                        slog.Warn("Target pattern compile error:", "pattern", targetPattern)
                                }
				matches := r.FindStringSubmatch(jsonMsg.ConnectionID)
				if len(matches) >= 2 {
					targetExt = matches[1]
				}
				slog.Debug("Target resolved from ConnectionID(json)", "id", jsonMsg.ConnectionID, "ext", targetExt)
			}
		}
	} else {
		if strings.Contains(msgStr, "MEDIA_START") {
			isMediaStart = true
		}
                targetPattern := "connection_id:" + appConfig.ExtenSearchPattern
                r, err := regexp.Compile(targetPattern)
                if err != nil {
                        slog.Warn("Target pattern compile error:", "pattern", targetPattern)
                }
		matches := r.FindStringSubmatch(msgStr)
		if len(matches) >= 2 {
			targetExt = matches[1]
		}
		slog.Debug("Target resolved from ConnectionID(text)", "msg", msgStr, "ext", targetExt)
	}

	if targetExt == "" {
		slog.Warn("Target Extension not found in message", "msg", msgStr)
		return
	}

	var session *ClientSession
	var groupErr error
	isGroupCall := false

	if grp, ok := groupConfig[targetExt]; ok {
		isGroupCall = true
		if grp.Strategy == "sequential" {
			session, groupErr = processSequential(targetExt, grp)
		} else {
			session, groupErr = processRingAll(targetExt, grp)
		}

		if groupErr != nil {
			slog.Warn("Group Call Failed", "ext", targetExt, "err", groupErr)
			conn.WriteMessage(websocket.TextMessage, createAsteriskCommand("HANGUP"))
			return
		}
	} else {
		s, exists := registry.Get(targetExt)
		if !exists {
			slog.Warn("Target not found (offline)", "ext", targetExt)
			return
		}
		if !s.TrySetState(StateRinging) {
			slog.Debug("Target is BUSY", "ext", targetExt)
			conn.WriteMessage(websocket.TextMessage, createAsteriskCommand("HANGUP"))
			return
		}
		session = s
	}

	defer func() {
		session.ResetState()
		slog.Debug("Bridge End", "ext", session.Extension)
		session.mu.Lock()
		session.Conn.WriteMessage(websocket.TextMessage, []byte("HANGUP"))
		session.mu.Unlock()
	}()

	errChan := make(chan error, 1)

	go func() {
		for {
			mt, payload, err := conn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			if session.GetState() == StateTalking {
				session.mu.Lock()
				session.Conn.WriteMessage(mt, payload)
				session.mu.Unlock()
			}
		}
	}()

	if isMediaStart {
		if isGroupCall {
			slog.Debug("Group Winner. Sending ANSWER", "winner", session.Extension)
			conn.WriteMessage(websocket.TextMessage, createAsteriskCommand("ANSWER"))
			if !session.TrySetState(StateTalking) {
				return
			}
		} else {
			slog.Debug("Incoming. Ringing...", "target", targetExt)
			session.Conn.WriteMessage(websocket.TextMessage, []byte("RINGING"))

			select {
			case cmd := <-session.ControlChan:
				if cmd == "ANSWER" {
					slog.Debug("User Answered", "ext", session.Extension)
					conn.WriteMessage(websocket.TextMessage, createAsteriskCommand("ANSWER"))
					session.TrySetState(StateTalking)
				} else {
					conn.WriteMessage(websocket.TextMessage, createAsteriskCommand("HANGUP"))
					return
				}
			case <-time.After(60 * time.Second):
				slog.Debug("Ringing Timeout", "ext", session.Extension)
				return
			case <-errChan:
				return
			}
		}

		slog.Debug("Bridge Start", "ext", session.Extension)

		for {
			select {
			case audioData, ok := <-session.AudioFromBrowser:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.BinaryMessage, audioData); err != nil {
					return
				}
			case cmd := <-session.ControlChan:
				if cmd == "HANGUP" {
					slog.Debug("User Hangup", "ext", session.Extension)
					conn.WriteMessage(websocket.TextMessage, createAsteriskCommand("HANGUP"))
					return
				}
			case err := <-errChan:
				slog.Debug("Asterisk Hangup", "cause", err)
				return
			}
		}

	} else {
		<-errChan
	}
}
