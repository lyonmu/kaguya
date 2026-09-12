package system

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/lyonmu/kaguya/internal/consts"
	"github.com/lyonmu/kaguya/internal/db"
	dto "github.com/lyonmu/kaguya/internal/dto/system"
	"github.com/lyonmu/kaguya/internal/ent/kaguyasysteminfo"
	"github.com/lyonmu/kaguya/pkg"
)

func tlsInfo(certPEM string) dto.TLSInfoResp {
	info := dto.TLSInfoResp{CertificatePEM: certPEM, Hosts: []string{}}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return info
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return info
	}
	sum := sha256.Sum256(cert.Raw)
	info.Fingerprint = hex.EncodeToString(sum[:])
	info.NotAfter = cert.NotAfter.UTC().Format(time.RFC3339)
	info.Hosts = append(info.Hosts, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		info.Hosts = append(info.Hosts, ip.String())
	}
	return info
}

// PrepareTLS runs after database initialization and before accepting any connections.
// Existing certificates are reused; invalid existing secrets never silently rotate.
func (s *SystemSvc) PrepareTLS(ctx context.Context, hosts []string) (*tls.Config, error) {
	row, err := db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
	if err != nil {
		return nil, err
	}
	if row.TLSCertificatePem == "" && row.TLSPrivateKeyPem == "" {
		cert, key, err := pkg.SelfSignedCertificate(hosts)
		if err != nil {
			return nil, err
		}
		_, err = db.EntClient.KaguyaSystemInfo.Update().Where(kaguyasysteminfo.IDEQ(consts.SystemInfoID), kaguyasysteminfo.TLSCertificatePemEQ(""), kaguyasysteminfo.TLSPrivateKeyPemEQ("")).SetTLSCertificatePem(cert).SetTLSPrivateKeyPem(key).Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("save initial TLS configuration: %w", err)
		}
		row, err = db.EntClient.KaguyaSystemInfo.Get(ctx, consts.SystemInfoID)
		if err != nil {
			return nil, err
		}
	}
	return pkg.ServerTLSConfig(row.TLSCertificatePem, row.TLSPrivateKeyPem)
}

func (s *SystemSvc) TLSUpdate(ctx context.Context, req *dto.TLSSaveReq) (*dto.TLSInfoResp, error) {
	cert, key := req.CertificatePEM, req.PrivateKeyPEM
	if req.Generate {
		if cert != "" || key != "" {
			return nil, ErrInvalidSystemInfo
		}
		var err error
		cert, key, err = pkg.SelfSignedCertificate(req.Hosts)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidSystemInfo, err)
		}
	} else if len(req.Hosts) > 0 {
		return nil, ErrInvalidSystemInfo
	}
	if _, err := pkg.ServerTLSConfig(cert, key); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidSystemInfo, err)
	}
	if err := db.EntClient.KaguyaSystemInfo.UpdateOneID(consts.SystemInfoID).
		SetTLSCertificatePem(cert).SetTLSPrivateKeyPem(key).Exec(ctx); err != nil {
		return nil, err
	}
	info := tlsInfo(cert)
	return &info, nil
}
