package system

type TLSInfoResp struct {
	CertificatePEM string   `json:"certificate_pem"`
	Fingerprint    string   `json:"fingerprint"`
	NotAfter       string   `json:"not_after"`
	Hosts          []string `json:"hosts"`
}

// Private keys are accepted only on writes, never returned by configuration queries.
type TLSSaveReq struct {
	Generate       bool     `json:"generate"`
	Hosts          []string `json:"hosts" binding:"max=32,dive,max=253"`
	CertificatePEM string   `json:"certificate_pem" binding:"max=131072"`
	PrivateKeyPEM  string   `json:"private_key_pem" binding:"max=32768"`
}
