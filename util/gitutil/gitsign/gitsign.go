package gitsign

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"io"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/moby/buildkit/util/gitutil/gitobject"
	"github.com/pkg/errors"
)

type sigType int

const (
	sigTypePGP sigType = iota
	sigTypeSSH
)

type Signature struct {
	PGPSignature *packet.Signature
}

type VerifyPolicy struct {
	RejectExpiredKeys bool
}

func VerifySignature(obj *gitobject.GitObject, pubKeyData []byte, policy *VerifyPolicy) error {
	ents, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(pubKeyData))
	if err != nil {
		return errors.Wrap(err, "failed to read armored public key")
	}

	sigBlock, st, err := parseSignatureBlock([]byte(obj.GPGSig))
	if err != nil {
		return err
	}
	if st != sigTypePGP {
		return errors.Errorf("unsupported signature type")
	}

	s, err := ParseSignature([]byte(obj.GPGSig))
	if err != nil {
		return err
	}
	sig := s.PGPSignature

	// add addition algorithm constraints
	if err := checkAlgoPolicy(sig); err != nil {
		return err
	}

	signer, err := openpgp.CheckDetachedSignature(
		ents,
		bytes.NewReader([]byte(obj.SignedData)),
		sigBlock,
		&packet.Config{},
	)
	if err != nil {
		if sig.IssuerKeyId != nil {
			return errors.Wrapf(err, "signature by %X", *sig.IssuerKeyId)
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
					return errors.Errorf("key expired at %v", expiry)
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
			return errors.Errorf("RSA key too short: %d bits", rsaPub.N.BitLen())
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
			return errors.Errorf("key revoked: %s", r.RevocationReasonText)
		}
		return errors.New("key revoked")
	}
	return nil
}

func parseSignatureBlock(data []byte) (io.Reader, sigType, error) {
	block, err := armor.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, errors.Wrap(err, "failed to decode armored signature")
	}
	switch block.Type {
	case "PGP SIGNATURE":
		return block.Body, sigTypePGP, nil
	case "SSH SIGNATURE":
		return block.Body, sigTypeSSH, nil
	default:
		return nil, 0, errors.Errorf("unknown block type: %s", block.Type)
	}
}

func ParseSignature(data []byte) (*Signature, error) {
	sigBlock, _, err := parseSignatureBlock(data)
	if err != nil {
		return nil, err
	}
	pr := packet.NewReader(sigBlock)
	for {
		p, err := pr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.Wrap(err, "failed to read next packet")
		}
		sig, ok := p.(*packet.Signature)
		if !ok {
			continue
		}
		return &Signature{sig}, nil
	}

	return nil, errors.Errorf("no signature packet found")
}

func checkAlgoPolicy(sig *packet.Signature) error {
	switch sig.Hash {
	case crypto.SHA256, crypto.SHA384, crypto.SHA512:
		// ok
	default:
		return errors.Errorf("rejecting weak/unknown hash: %v", sig.Hash)
	}
	// Pubkey policy
	switch sig.PubKeyAlgo {
	case packet.PubKeyAlgoEdDSA, packet.PubKeyAlgoECDSA, packet.PubKeyAlgoRSA, packet.PubKeyAlgoRSASignOnly:
	default:
		return errors.Errorf("rejecting unsupported pubkey algorithm: %v", sig.PubKeyAlgo)
	}
	return nil
}

func checkCreationTime(sigTime, now time.Time) error {
	if sigTime.After(now.Add(5 * time.Minute)) {
		return errors.Errorf("signature creation time is in the future: %v", sigTime)
	}
	return nil
}
