package ipc

// The wire format matches the rest of the dux family (dux-files / dux-calendar), so the QML
// side can reuse DSocket as-is:
//
//	on connect, server sends  {"apiVersion":1,"capabilities":[...]}
//	client request            {"id":3,"method":"session.list","params":{...}}
//	server response           {"id":3,"result":{...}}  or {"id":3,"error":"..."}
//	server event              {"event":"agent","data":{...}}
//	server reverse request    {"rid":5,"method":"permission.request","params":{...}}
//	client answer             {"rid":5,"result":{...}}  or {"rid":5,"error":"..."}
//
// Every line is one complete JSON object terminated by \n.
//
// The two id spaces are separate on purpose (id is client-initiated, rid server-initiated), so
// bidirectional requests on one connection cannot collide and no second connection is needed.

// Capabilities is the first line after a connection is established.
type Capabilities struct {
	APIVersion   int      `json:"apiVersion"`
	Capabilities []string `json:"capabilities"`
	Version      string   `json:"version,omitempty"`
	AppName      string   `json:"appName,omitempty"`
	// PID lets the client handle "the daemon behind this is already an old build": it is setsid'd so
	// closing the UI leaves it running, and a rebuild has to be able to ask it to step aside.
	PID int `json:"pid,omitempty"`
}

// Request is one line from the client.
//
// A non-zero RID means this line is **not** a request but an answer to a server reverse request
// (see ReverseRequest). Method is then empty and Result/Error carry the content.
type Request struct {
	ID     int            `json:"id,omitempty"`
	RID    int            `json:"rid,omitempty"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
	Result any            `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// IsReply reports whether this line answers a reverse request.
func (r Request) IsReply() bool { return r.RID != 0 && r.Method == "" }

// Response is one line back to the client.
type Response struct {
	ID     int    `json:"id,omitempty"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ReverseRequest is one line the server sends first, which the client must answer.
//
// TimeoutMs is only a hint for the client (roughly how long until the question expires); the
// real timeout is enforced on the server, so the client needs no timer of its own.
type ReverseRequest struct {
	RID       int            `json:"rid"`
	Method    string         `json:"method"`
	Params    map[string]any `json:"params,omitempty"`
	TimeoutMs int            `json:"timeoutMs,omitempty"`
}

// ReverseReply is the client's answer to a ReverseRequest.
type ReverseReply struct {
	RID    int    `json:"rid"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Event is one line the server pushes.
type Event struct {
	Topic string `json:"event"`
	Data  any    `json:"data"`
}

// Get reads one param value, returning the zero value on a type mismatch.
func (r Request) Get(key string) any {
	if r.Params == nil {
		return nil
	}
	return r.Params[key]
}

// GetString returns a string, empty when absent.
func (r Request) GetString(key string) string {
	if v, ok := r.Get(key).(string); ok {
		return v
	}
	return ""
}

// GetBool returns a bool, false when absent.
func (r Request) GetBool(key string) bool {
	if v, ok := r.Get(key).(bool); ok {
		return v
	}
	return false
}

// GetInt returns an int; JSON numbers are always float64, handled here.
func (r Request) GetInt(key string) int {
	switch v := r.Get(key).(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// GetStrings returns a string slice; a non-array yields nil and non-string elements are
// skipped.
func (r Request) GetStrings(key string) []string {
	raw, ok := r.Get(key).([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
