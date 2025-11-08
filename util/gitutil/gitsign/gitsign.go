package gitsign

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/hiddeco/sshsig"
	"github.com/moby/buildkit/util/gitutil/gitobject"
	"golang.org/x/crypto/ssh"
)

type sigType int

const (
	sigTypePGP sigType = iota
	sigTypeSSH
)

type Signature struct {
	PGPSignature *packet.Signature
	SSHSignature *sshsig.Signature
}

type VerifyPolicy struct {
	RejectExpiredKeys bool
}

func VerifySignature(obj *gitobject.GitObject, pubKeyData []byte, policy *VerifyPolicy) error {
	if len(obj.Signature) == 0 {
		return errors.New("git object is not signed")
	}

	s, err := ParseSignature([]byte(obj.Signature))
	if err != nil {
		return err
	}
	if s.PGPSignature != nil {
		return verifyPGPSignature(obj, s.PGPSignature, pubKeyData, policy)
	} else if s.SSHSignature != nil {
		return verifySSHSignature(obj, s.SSHSignature, pubKeyData)
	}
	return errors.New("no valid signature found")
}

func verifyPGPSignature(obj *gitobject.GitObject, sig *packet.Signature, pubKeyData []byte, policy *VerifyPolicy) error {
	sigBlock, _, err := parseSignatureBlock([]byte(obj.Signature))
	if err != nil {
		return err
	}

	ents, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(pubKeyData))
	if err != nil {
		return fmt.Errorf("failed to read armored public key"+": %w", err)
	}

	// add addition algorithm constraints
	if err := checkAlgoPolicy(sig); err != nil {
		return err
	}

	signer, err := openpgp.CheckDetachedSignature(
		ents,
		bytes.NewReader([]byte(obj.SignedData)),
		bytes.NewReader(sigBlock),
		&packet.Config{},
	)
	if err != nil {
		if sig.IssuerKeyId != nil {
			return fmt.Errorf("signature by %X: %w", *sig.IssuerKeyId, err)
		}
		return err
	}

	if err := checkEntityUsableForSigning(signer, time.Now(), policy); err != nil {
		return err
	}

	if err := checkCreationTime(sig.CreationTime, time.Now()); err != nil {
		return err
	}

	return nil
}

func verifySSHSignature(obj *gitobject.GitObject, sig *sshsig.Signature, pubKeyData []byte) error {
	// future proofing
	if sig.Version != 1 {
		return fmt.Errorf("unsupported SSH signature version: %d", sig.Version)
	}

	switch sig.HashAlgorithm {
	case sshsig.HashSHA256, sshsig.HashSHA512:
		// OK
	default:
		return fmt.Errorf("unsupported SSH signature hash algorithm: %s", sig.HashAlgorithm)
	}
	if sig.Namespace != "git" {
		return fmt.Errorf("unexpected SSH signature namespace: %q", sig.Namespace)
	}

	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(pubKeyData)
	if err != nil {
		return fmt.Errorf("failed to parse ssh public key"+": %w", err)
	}

	if err := sshsig.Verify(strings.NewReader(obj.SignedData), sig, pubKey, sig.HashAlgorithm, sig.Namespace); err != nil {
		return fmt.Errorf("failed to verify ssh signature"+": %w", err)
	}
	return nil
}

func checkEntityUsableForSigning(e *openpgp.Entity, now time.Time, policy *VerifyPolicy) error {
	if e == nil || e.PrimaryKey == nil {
		return errors.New("nil entity or key")
	}

	// Expiry
	if policy != nil && policy.RejectExpiredKeys {
		if id := e.PrimaryIdentity(); id != nil && id.SelfSignature != nil {
			if exp := id.SelfSignature.KeyLifetimeSecs; exp != nil && *exp > 0 {
				expiry := e.PrimaryKey.CreationTime.Add(time.Duration(*exp) * time.Second)
				if now.After(expiry) {
					return fmt.Errorf("key expired at %v", expiry)
				}
			}
		}
	}

	// Revocation
	if err := checkEntityRevocation(e); err != nil {
		return err
	}

	// RSA bit length (optional)
	if rsaPub, ok := e.PrimaryKey.PublicKey.(*rsa.PublicKey); ok {
		if rsaPub.N.BitLen() < 2048 {
			return fmt.Errorf("RSA key too short: %d bits", rsaPub.N.BitLen())
		}
	}

	return nil
}

func checkEntityRevocation(e *openpgp.Entity) error {
	if e == nil {
		return nil
	}
	for _, r := range e.Revocations {
		if r == nil || r.SigType != packet.SigTypeKeyRevocation {
			continue
		}
		if err := e.PrimaryKey.VerifyRevocationSignature(r); err != nil {
			continue // ignore malformed or unverified revocations
		}
		if r.RevocationReasonText != "" {
			return fmt.Errorf("key revoked: %s", r.RevocationReasonText)
		}
		return errors.New("key revoked")
	}
	return nil
}

func parseSignatureBlock(data []byte) ([]byte, sigType, error) {
	if strings.HasPrefix(string(data), "-----BEGIN SSH SIGNATURE-----") {
		block, _ := pem.Decode(data)
		if block == nil || block.Type != "SSH SIGNATURE" {
			return nil, 0, errors.New("failed to decode ssh signature PEM block")
		}
		return block.Bytes, sigTypeSSH, nil
	} else if strings.HasPrefix(string(data), "-----BEGIN PGP SIGNATURE-----") {
		block, err := armor.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to decode armored signature"+": %w", err)
		}
		dt, err := io.ReadAll(block.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read armored signature body"+": %w", err)
		}
		return dt, sigTypePGP, nil
	}
	return nil, 0, errors.New("invalid signature format")
}

func ParseSignature(data []byte) (*Signature, error) {
	sigBlock, typ, err := parseSignatureBlock(data)
	if err != nil {
		return nil, err
	}
	switch typ {
	case sigTypePGP:
		pr := packet.NewReader(bytes.NewReader(sigBlock))
		for {
			p, err := pr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("failed to read next packet"+": %w", err)
			}
			sig, ok := p.(*packet.Signature)
			if !ok {
				continue
			}
			return &Signature{PGPSignature: sig}, nil
		}
	case sigTypeSSH:
		sig, err := sshsig.ParseSignature(sigBlock)
		if err != nil {
			return nil, fmt.Errorf("failed to parse ssh signature"+": %w", err)
		}
		return &Signature{SSHSignature: sig}, nil
	}

	return nil, errors.New("no signature packet found")
}

func checkAlgoPolicy(sig *packet.Signature) error {
	switch sig.Hash {
	case crypto.SHA256, crypto.SHA384, crypto.SHA512:
		// ok
	default:
		return fmt.Errorf("rejecting weak/unknown hash: %v", sig.Hash)
	}
	// Pubkey policy
	switch sig.PubKeyAlgo {
	case packet.PubKeyAlgoEdDSA, packet.PubKeyAlgoECDSA, packet.PubKeyAlgoRSA, packet.PubKeyAlgoRSASignOnly:
	default:
		return fmt.Errorf("rejecting unsupported pubkey algorithm: %v", sig.PubKeyAlgo)
	}
	return nil
}

func checkCreationTime(sigTime, now time.Time) error {
	if sigTime.After(now.Add(5 * time.Minute)) {
		return fmt.Errorf("signature creation time is in the future: %v", sigTime)
	}
	return nil
}
