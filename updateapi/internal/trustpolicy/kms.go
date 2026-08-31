package trustpolicy

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"strings"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	kms "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/kms/v20190118"
)

const kmsRegion = "ap-shanghai"

const (
	kmsProviderEnvironmentSession = "environment-session"
	kmsProviderTKEOIDC            = "tke-oidc"
	kmsProviderCVMRole            = "cvm-role"
)

type kmsAPI interface {
	GetPublicKeyWithContext(context.Context, *kms.GetPublicKeyRequest) (*kms.GetPublicKeyResponse, error)
	SignByAsymmetricKeyWithContext(context.Context, *kms.SignByAsymmetricKeyRequest) (*kms.SignByAsymmetricKeyResponse, error)
}

// KMSSigner adapts the pinned Tencent KMS v20190118 API to the narrow Signer
// boundary and validates provider encodings before returning any bytes.
type KMSSigner struct {
	client             kmsAPI
	expectedSPKISHA256 string
}

type kmsProviderFactories struct {
	environmentSession func() (common.Provider, error)
	tkeOIDC            func() (common.Provider, error)
	cvmRole            func() (common.Provider, error)
	newClient          func(common.CredentialIface, string) (kmsAPI, error)
}

type environmentSessionProvider struct {
	lookup func(string) (string, bool)
}

// NewKMSSigner constructs a testable adapter around a Tencent KMS client.
func NewKMSSigner(client kmsAPI, expectedSPKISHA256 string) (*KMSSigner, error) {
	if client == nil || !sha256Hex.MatchString(expectedSPKISHA256) {
		return nil, errReviewedInput
	}
	return &KMSSigner{client: client, expectedSPKISHA256: expectedSPKISHA256}, nil
}

// ValidKMSProviderMode reports whether mode is one closed, reviewed
// short-lived credential source.
func ValidKMSProviderMode(mode string) bool {
	return mode == kmsProviderEnvironmentSession || mode == kmsProviderTKEOIDC || mode == kmsProviderCVMRole
}

// NewTencentKMSSigner selects exactly one explicit SDK-managed short-lived
// credential source. It never falls through to ambient SDK providers.
func NewTencentKMSSigner(region, expectedSPKISHA256, mode string) (Signer, error) {
	return newTencentKMSSignerWithProviders(region, expectedSPKISHA256, mode, kmsProviderFactories{
		environmentSession: func() (common.Provider, error) {
			return newEnvironmentSessionProvider(os.LookupEnv), nil
		},
		tkeOIDC: func() (common.Provider, error) {
			return common.DefaultTkeOIDCRoleArnProvider()
		},
		cvmRole: func() (common.Provider, error) {
			return common.DefaultCvmRoleProvider(), nil
		},
		newClient: func(credential common.CredentialIface, region string) (kmsAPI, error) {
			return kms.NewClient(credential, region, profile.NewClientProfile())
		},
	})
}

func newTencentKMSSignerWithProviders(region, expectedSPKISHA256, mode string, factories kmsProviderFactories) (Signer, error) {
	if region != kmsRegion || !sha256Hex.MatchString(expectedSPKISHA256) || !ValidKMSProviderMode(mode) || factories.newClient == nil {
		return nil, errReviewedInput
	}
	var factory func() (common.Provider, error)
	switch mode {
	case kmsProviderEnvironmentSession:
		factory = factories.environmentSession
	case kmsProviderTKEOIDC:
		factory = factories.tkeOIDC
	case kmsProviderCVMRole:
		factory = factories.cvmRole
	}
	if factory == nil {
		return nil, errReviewedInput
	}
	provider, err := factory()
	if err != nil || provider == nil {
		return nil, errReviewedInput
	}
	credential, err := common.NewProviderChain([]common.Provider{provider}).GetCredential()
	if err != nil || credential == nil {
		return nil, errReviewedInput
	}
	_, _, token := credential.GetCredential()
	if strings.TrimSpace(token) == "" {
		return nil, errReviewedInput
	}
	client, err := factories.newClient(credential, region)
	if err != nil || client == nil {
		return nil, errReviewedInput
	}
	return NewKMSSigner(client, expectedSPKISHA256)
}

func newEnvironmentSessionProvider(lookup func(string) (string, bool)) common.Provider {
	return &environmentSessionProvider{lookup: lookup}
}

func (provider *environmentSessionProvider) GetCredential() (common.CredentialIface, error) {
	if provider == nil || provider.lookup == nil {
		return nil, errReviewedInput
	}
	secretID, idOK := provider.lookup("TENCENTCLOUD_SECRET_ID")
	secretKey, keyOK := provider.lookup("TENCENTCLOUD_SECRET_KEY")
	token, tokenOK := provider.lookup("TENCENTCLOUD_SESSION_TOKEN")
	if !idOK || !keyOK || !tokenOK || strings.TrimSpace(secretID) == "" || strings.TrimSpace(secretKey) == "" || strings.TrimSpace(token) == "" {
		return nil, errReviewedInput
	}
	return common.NewTokenCredential(secretID, secretKey, token), nil
}

func (signer *KMSSigner) PublicKey(ctx context.Context, keyID string) ([]byte, string, error) {
	if signer == nil || signer.client == nil || ctx == nil || !keyIDValue.MatchString(keyID) {
		return nil, "", errPublicKey
	}
	request := kms.NewGetPublicKeyRequest()
	request.KeyId = common.StringPtr(keyID)
	response, err := signer.client.GetPublicKeyWithContext(ctx, request)
	if err != nil || response == nil || response.Response == nil || response.Response.KeyId == nil ||
		response.Response.PublicKey == nil || response.Response.PublicKeyPem == nil || response.Response.RequestId == nil ||
		*response.Response.KeyId != keyID || !requestID.MatchString(*response.Response.RequestId) {
		return nil, "", errPublicKey
	}
	der, err := base64.StdEncoding.Strict().DecodeString(*response.Response.PublicKey)
	if err != nil || len(der) == 0 {
		return nil, "", errPublicKey
	}
	block, rest := pem.Decode([]byte(*response.Response.PublicKeyPem))
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 || !bytes.Equal(block.Bytes, der) {
		return nil, "", errPublicKey
	}
	if _, err := parseReviewedPublicKey(der, signer.expectedSPKISHA256); err != nil {
		return nil, "", errPublicKey
	}
	return append([]byte(nil), der...), *response.Response.RequestId, nil
}

func (signer *KMSSigner) SignDigest(ctx context.Context, keyID string, digest []byte) ([]byte, string, error) {
	if signer == nil || signer.client == nil || ctx == nil || !keyIDValue.MatchString(keyID) || len(digest) != 32 {
		return nil, "", errSigning
	}
	request := kms.NewSignByAsymmetricKeyRequest()
	request.KeyId = common.StringPtr(keyID)
	request.Algorithm = common.StringPtr("ECC_P256_R1")
	request.MessageType = common.StringPtr("DIGEST")
	request.Message = common.StringPtr(base64.StdEncoding.EncodeToString(digest))
	response, err := signer.client.SignByAsymmetricKeyWithContext(ctx, request)
	if err != nil || response == nil || response.Response == nil || response.Response.Signature == nil || response.Response.RequestId == nil ||
		!requestID.MatchString(*response.Response.RequestId) {
		return nil, "", errSigning
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(*response.Response.Signature)
	if err != nil || !validP256DERSignature(signature) {
		return nil, "", errSigning
	}
	return append([]byte(nil), signature...), *response.Response.RequestId, nil
}

func validP256DERSignature(signature []byte) bool {
	var parsed struct {
		R *big.Int
		S *big.Int
	}
	rest, err := asn1.Unmarshal(signature, &parsed)
	if err != nil || len(rest) != 0 || parsed.R == nil || parsed.S == nil || parsed.R.Sign() <= 0 || parsed.S.Sign() <= 0 {
		return false
	}
	order := elliptic.P256().Params().N
	return parsed.R.Cmp(order) < 0 && parsed.S.Cmp(order) < 0
}
