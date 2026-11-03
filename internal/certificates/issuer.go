package certificates

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"golang.org/x/crypto/acme"
)

const ProductionDirectory = "https://acme-v02.api.letsencrypt.org/directory"
const StagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"

type DNS interface {
	Present(context.Context, string, string) (string, error)
	Wait(context.Context, string, string) error
	Cleanup(context.Context, string) error
}
type Issuer struct {
	Directory  string
	HTTPClient *http.Client
	Now        func() time.Time
}

func NewAccountKey() (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})), nil
}

// Issue uses x/crypto's RFC 8555 implementation. Its pinned version has no ARI
// support, so renewal scheduling uses the conservative two-thirds lifetime point.
func (i Issuer) Issue(ctx context.Context, accountKey, email, domain string, dns DNS) (*model.CertificateBundle, error) {
	block, _ := pem.Decode([]byte(accountKey))
	if block == nil {
		return nil, errors.New("ACME 账号密钥无效")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("ACME 账号密钥无效")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("ACME 账号密钥类型无效")
	}
	directory := i.Directory
	if directory == "" {
		directory = ProductionDirectory
	}
	httpClient := i.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	client := &acme.Client{Key: key, DirectoryURL: directory, HTTPClient: httpClient, UserAgent: "HQIP certificate manager"}
	if _, err = client.Register(ctx, &acme.Account{Contact: []string{"mailto:" + email}}, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, errors.New("ACME 账号注册失败，请检查网络及联系邮箱")
	}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(domain))
	if err != nil {
		return nil, errors.New("ACME 证书订单创建失败，请检查域名及签发限额")
	}
	for _, authURL := range order.AuthzURLs {
		auth, err := client.GetAuthorization(ctx, authURL)
		if err != nil {
			return nil, errors.New("ACME 域名授权读取失败")
		}
		if auth.Identifier.Type != "dns" || auth.Identifier.Value != domain || auth.Wildcard {
			return nil, errors.New("ACME 返回了非本节点域名的授权")
		}
		if auth.Status == acme.StatusValid {
			continue
		}
		var challenge *acme.Challenge
		for _, ch := range auth.Challenges {
			if ch.Type == "dns-01" {
				challenge = ch
				break
			}
		}
		if challenge == nil {
			return nil, errors.New("ACME 未提供 DNS-01 验证")
		}
		value, err := client.DNS01ChallengeRecord(challenge.Token)
		if err != nil {
			return nil, errors.New("ACME 挑战生成失败")
		}
		name := "_acme-challenge." + domain
		record, err := dns.Present(ctx, name, value)
		if err != nil {
			return nil, err
		}
		// Cleanup is bounded and independent of the cancelled issuance context;
		// its durable DNS journal is retried later if the API is unavailable.
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_ = dns.Cleanup(cleanupCtx, record)
		}()
		if err = dns.Wait(ctx, name, value); err != nil {
			return nil, err
		}
		if _, err = client.Accept(ctx, challenge); err != nil {
			return nil, errors.New("ACME DNS 验证提交失败")
		}
		if _, err = client.WaitAuthorization(ctx, authURL); err != nil {
			return nil, errors.New("ACME DNS 验证失败，请检查权威解析")
		}
	}
	order, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return nil, errors.New("ACME 证书订单未就绪")
	}
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{domain}}, certKey)
	if err != nil {
		return nil, err
	}
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return nil, errors.New("ACME 证书签发失败")
	}
	bundle := &model.CertificateBundle{Domain: domain}
	for _, part := range der {
		bundle.CertificatePEM += string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: part}))
	}
	private, err := x509.MarshalPKCS8PrivateKey(certKey)
	if err != nil {
		return nil, err
	}
	bundle.PrivateKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	now := time.Now()
	if i.Now != nil {
		now = i.Now()
	}
	leaf, err := Validate(bundle, now)
	if err != nil {
		return nil, err
	}
	bundle.NotBefore = leaf.NotBefore
	bundle.NotAfter = leaf.NotAfter
	return bundle, nil
}

func Validate(bundle *model.CertificateBundle, now time.Time) (*x509.Certificate, error) {
	if bundle == nil || len(bundle.CertificatePEM) > 128<<10 || len(bundle.PrivateKeyPEM) > 16<<10 {
		return nil, errors.New("证书包无效")
	}
	pair, err := tls.X509KeyPair([]byte(bundle.CertificatePEM), []byte(bundle.PrivateKeyPEM))
	if err != nil || len(pair.Certificate) == 0 {
		return nil, errors.New("证书与私钥不匹配")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("证书内容无效")
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != bundle.Domain || len(leaf.IPAddresses) > 0 || leaf.VerifyHostname(bundle.Domain) != nil {
		return nil, errors.New("证书不属于当前节点域名")
	}
	if now.Before(leaf.NotBefore.Add(-5*time.Minute)) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("证书不在有效期内")
	}
	return leaf, nil
}
func RenewalTime(notBefore, notAfter time.Time) time.Time {
	return notBefore.Add(notAfter.Sub(notBefore) * 2 / 3)
}
func RetryDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > 8 {
		failures = 8
	}
	delay := time.Duration(1<<uint(failures-1)) * 15 * time.Minute
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return delay
}
func ExpiryWarning(now, expiry time.Time) string {
	if expiry.IsZero() {
		return ""
	}
	remaining := expiry.Sub(now)
	switch {
	case remaining <= 0:
		return "证书已过期"
	case remaining <= 3*24*time.Hour:
		return "证书将在 3 天内过期"
	case remaining <= 7*24*time.Hour:
		return "证书将在 7 天内过期"
	case remaining <= 14*24*time.Hour:
		return "证书将在 14 天内过期"
	}
	return ""
}
