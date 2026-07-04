package constant

// CtxKey is the type for context value keys to avoid built-in string key
// collisions (staticcheck SA1029).
type CtxKey string

const (
	CtxUserID   CtxKey = "user_id"
	CtxTraceID  CtxKey = "trace_id"
	CtxService  CtxKey = "service"
	CtxClientIP CtxKey = "client_ip"
)

const (
	MaxBatchSize = 500
	MaxVersion   = 10000000
	ExpectedSize = 16
)

const (
	ASC  = "ASC"
	DESC = "DESC"
)

const (
	MaxOpenConn = 100
	MaxIdleConn = 100
)
