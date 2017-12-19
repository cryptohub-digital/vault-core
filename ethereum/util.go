package ethereum

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	accounts "github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/hashicorp/vault/logical"
)

const (
	PathTempDir         string = "/tmp/"
	ProtocolKeystore    string = "keystore://"
	MaxKeystoreSize     int64  = 1024 // Just a heuristic to prevent reading stupid big files
	RequestPathImport   string = "import"
	RequestPathAccounts string = "accounts"
)

func (b *backend) buildKeystoreURL(filename string) string {
	return ProtocolKeystore + PathTempDir + filename
}

func (b *backend) writeTemporaryKeystoreFile(path string, data []byte) error {
	return ioutil.WriteFile(path, data, 0644)
}

func (b *backend) createTemporaryKeystore(name string) (string, error) {
	file, _ := os.Open(PathTempDir + name)
	if file != nil {
		file.Close()
		return "", fmt.Errorf("account already exists at %s", PathTempDir+name)
	}
	return PathTempDir + name, os.MkdirAll(PathTempDir+name, os.FileMode(0522))
}

func (b *backend) removeTemporaryKeystore(name string) error {
	file, _ := os.Open(PathTempDir + name)
	if file != nil {
		return os.RemoveAll(PathTempDir + name)
	} else {
		return fmt.Errorf("keystore doesn't exist at %s", PathTempDir+name)
	}
}

func convertMapToStringValue(initial map[string]interface{}) map[string]string {
	result := map[string]string{}
	for key, value := range initial {
		result[key] = fmt.Sprintf("%v", value)
	}
	return result
}

func parseURL(url string) (accounts.URL, error) {
	parts := strings.Split(url, "://")
	if len(parts) != 2 || parts[0] == "" {
		return accounts.URL{}, errors.New("protocol scheme missing")
	}
	return accounts.URL{
		Scheme: parts[0],
		Path:   parts[1],
	}, nil
}

func (b *backend) rekeyJSONKeystore(keystorePath string, passphrase string, newPassphrase string) ([]byte, error) {
	var key *keystore.Key
	jsonKeystore, err := b.readJSONKeystore(keystorePath)
	if err != nil {
		return nil, err
	}
	key, _ = keystore.DecryptKey(jsonKeystore, passphrase)

	if key != nil && key.PrivateKey != nil {
		defer zeroKey(key.PrivateKey)
	}
	jsonBytes, err := keystore.EncryptKey(key, newPassphrase, keystore.StandardScryptN, keystore.StandardScryptP)
	return jsonBytes, err
}

func (b *backend) readKeyFromJSONKeystore(keystorePath string, passphrase string) (*keystore.Key, error) {
	var key *keystore.Key
	jsonKeystore, err := b.readJSONKeystore(keystorePath)
	if err != nil {
		return nil, err
	}
	key, _ = keystore.DecryptKey(jsonKeystore, passphrase)

	if key != nil && key.PrivateKey != nil {
		return key, nil
	} else {
		return nil, fmt.Errorf("failed to read key from keystore")
	}
}

func zeroKey(k *ecdsa.PrivateKey) {
	b := k.D.Bits()
	for i := range b {
		b[i] = 0
	}
}

func (b *backend) importJSONKeystore(keystorePath string, passphrase string) (string, []byte, error) {
	b.Logger().Info("importJSONKeystore", "keystorePath", keystorePath)
	var key *keystore.Key
	jsonKeystore, err := b.readJSONKeystore(keystorePath)
	if err != nil {
		return "", nil, err
	}
	key, err = keystore.DecryptKey(jsonKeystore, passphrase)

	if key != nil && key.PrivateKey != nil {
		defer zeroKey(key.PrivateKey)
	}
	return key.Address.Hex(), jsonKeystore, err
}

func pathExists(req *logical.Request, path string) (bool, error) {
	out, err := req.Storage.Get(path)
	if err != nil {
		return false, fmt.Errorf("existence check failed for %s: %v", path, err)
	}

	return out != nil, nil
}

func (b *backend) readJSONKeystore(keystorePath string) ([]byte, error) {
	b.Logger().Info("readJSONKeystore", "keystorePath", keystorePath)
	var jsonKeystore []byte
	file, err := os.Open(keystorePath)
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if stat.Size() > MaxKeystoreSize {
		err = fmt.Errorf("keystore is suspiciously large at %d bytes", stat.Size())
		return nil, err
	} else {
		jsonKeystore, err = ioutil.ReadFile(keystorePath)
		if err != nil {
			return nil, err
		}
		return jsonKeystore, nil
	}
}

func (b *backend) NewTransactor(key *ecdsa.PrivateKey) *bind.TransactOpts {
	keyAddr := crypto.PubkeyToAddress(key.PublicKey)
	return &bind.TransactOpts{
		From: keyAddr,
		Signer: func(signer types.Signer, address common.Address, tx *types.Transaction) (*types.Transaction, error) {
			if address != keyAddr {
				return nil, errors.New("not authorized to sign this account")
			}
			signature, err := crypto.Sign(signer.Hash(tx).Bytes(), key)
			if err != nil {
				return nil, err
			}
			return tx.WithSignature(signer, signature)
		},
	}
}

func (b *backend) readAccount(req *logical.Request, path string) (*Account, error) {
	accountEntry, err := req.Storage.Get(path)
	if err != nil {
		return nil, err
	}

	var account Account
	err = accountEntry.DecodeJSON(&account)

	if err != nil {
		return nil, err
	}
	if accountEntry == nil {
		return nil, nil
	}
	return &account, nil
}

func (b *backend) getAccountPrivateKey(path string, account Account) (*keystore.Key, error) {
	_, err := b.createTemporaryKeystore(path)
	if err != nil {
		return nil, err
	}
	keystorePath := strings.Replace(account.KeystoreURL, ProtocolKeystore, "", -1)
	b.writeTemporaryKeystoreFile(keystorePath, account.JSONKeystore)
	key, err := b.readKeyFromJSONKeystore(keystorePath, account.Passphrase)
	b.removeTemporaryKeystore(path)
	return key, nil
}

func (b *backend) exportKeystore(path string, accountPath string, account *Account) (string, error) {
	keystorePath := strings.Replace(account.KeystoreURL, ProtocolKeystore, "", -1)
	b.Logger().Info("Directory", "path", path)
	b.Logger().Info("Starting Keystore path", "keystorePath", keystorePath)
	directory := path
	pieces := strings.Split(keystorePath, "/tmp/"+accountPath+"/")
	if len(pieces) == 2 {
		b.Logger().Info("Piece", "pieces[1]", pieces[1])
		if !strings.HasSuffix(path, "/") {
			directory = directory + "/" + pieces[1]
		} else {
			directory = directory + pieces[1]
		}
	} else {
		return "", fmt.Errorf("can't parse path: %s", keystorePath)
	}
	b.Logger().Info("Keystore path", "directory", directory)
	b.writeTemporaryKeystoreFile(directory, account.JSONKeystore)
	return directory, nil
}
