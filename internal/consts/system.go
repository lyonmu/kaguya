package consts

type Status int

const (
	IsUnknown Status = -1
	IsTrue    Status = 1
	IsFalse   Status = 2
)

type DBKind string

const (
	MySQL  DBKind = "mysql"
	SQLite DBKind = "sqlite"
)
