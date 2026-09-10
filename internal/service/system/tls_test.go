package system

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dto "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/pkg"
)

func TestTLSStoredAndReloadedWithoutExposingPrivateKey(t *testing.T) {
	ctx := setupSystemServiceTest(t)
	svc := &SystemSvc{}
	first, err := svc.PrepareTLS(ctx, []string{"agent.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.PrepareTLS(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Certificates[0].Certificate[0], second.Certificates[0].Certificate[0]) {
		t.Fatal("restart regenerated existing certificate")
	}
	info, err := svc.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(info)
	row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TLSPrivateKeyPem == "" || bytes.Contains(data, []byte("PRIVATE KEY")) || bytes.Contains(data, []byte("private_key")) {
		t.Fatal("missing stored private key or leaked response")
	}
	newCert, newKey, err := pkg.SelfSignedCertificate([]string{"new.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TLSUpdate(ctx, &dto.TLSSaveReq{CertificatePEM: newCert, PrivateKeyPEM: row.TLSPrivateKeyPem}); err == nil {
		t.Fatal("mismatched certificate saved")
	}
	unchanged, _ := svc.Info(ctx)
	if unchanged.TLS.Fingerprint != info.TLS.Fingerprint {
		t.Fatal("invalid save changed certificate")
	}
	if _, err := svc.TLSUpdate(ctx, &dto.TLSSaveReq{CertificatePEM: newCert, PrivateKeyPEM: newKey}); err != nil {
		t.Fatal(err)
	}
	third, err := svc.PrepareTLS(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Certificates[0].Certificate[0], third.Certificates[0].Certificate[0]) {
		t.Fatal("restart did not load changed certificate")
	}
	if !bytes.Equal(first.Certificates[0].Certificate[0], second.Certificates[0].Certificate[0]) {
		t.Fatal("save changed active TLS snapshot")
	}
	if _, err := svc.TLSUpdate(ctx, &dto.TLSSaveReq{Generate: true, Hosts: []string{"renew.example.com"}}); err != nil {
		t.Fatal(err)
	}
	// A partially missing configuration must fail closed instead of changing identity.
	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).SetTLSPrivateKeyPem("").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PrepareTLS(ctx, nil); err == nil {
		t.Fatal("partial TLS configuration silently replaced")
	}
}
