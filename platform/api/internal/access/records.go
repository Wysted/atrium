package access

// NotifyChannel is the PostgreSQL channel notified by any change that can alter an access decision.
const NotifyChannel = "access_changed"

// Profile is the informational user data carried by the assertion. It never identifies the user.
type Profile struct {
	Email string
	Name  string
}

// Denial is one deny decision to be recorded in access_denials. Empty optional fields are stored as NULL.
type Denial struct {
	UserID       string
	Host         string
	AppID        string
	InitiativeID string
	Reason       string
	SessionID    string
}
