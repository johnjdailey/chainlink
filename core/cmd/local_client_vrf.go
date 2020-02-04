package cmd

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/pkg/errors"
	clipkg "github.com/urfave/cli"

	"chainlink/core/logger"
	"chainlink/core/store"
	"chainlink/core/store/models/vrfkey"
	"chainlink/core/utils"
)

func vRFKeyStore(cli *Client) *store.VRFKeyStore {
	return cli.AppFactory.NewApplication(cli.Config).GetStore().VRFKeyStore
}

// CreateVRFKey creates a key in the VRF keystore, protected by the password in
// the password file
func (cli *Client) CreateVRFKey(c *clipkg.Context) error {
	password, err := getPassword(c)
	if err != nil {
		return err
	}
	key, err := vRFKeyStore(cli).CreateKey(string(password))
	if err != nil {
		return errors.Wrapf(err, "while creating new account")
	}
	fmt.Printf(`Created keypair, with public key

%s

The following command will export the encrypted secret key from the db to <save_path>:

chainlink local vrf export -f <save_path> -pk %s
`, key, key)
	return nil
}

// CreateAndExportWeakVRFKey creates a key in the VRF keystore, protected by the
// password in the password file, but with weak key-derivation-function
// parameters, which makes it cheaper for testing, but also more vulnerable to
// bruteforcing of the encyrpted key material. For testing purposes only!
//
// The key is only stored at the specified file location, not stored in the DB.
func (cli *Client) CreateAndExportWeakVRFKey(c *clipkg.Context) error {
	password, err := getPassword(c)
	if err != nil {
		return err
	}
	key, err := vRFKeyStore(cli).CreateWeakInMemoryEncryptedKeyXXXTestingOnly(
		string(password))
	if err != nil {
		return errors.Wrapf(err, "while creating testing key")
	}
	if !c.IsSet("file") || !noFileToOverwrite(c.String("file")) {
		errmsg := "must specify path to key file which does not already exist"
		fmt.Println(errmsg)
		return fmt.Errorf(errmsg)
	}
	fmt.Println("Don't use this key for anything sensitive!")
	return key.WriteToDisk(c.String("file"))
}

// getPassword retrieves the password from the file specified on the CL, or errors
func getPassword(c *clipkg.Context) ([]byte, error) {
	if !c.IsSet("password") {
		return nil, fmt.Errorf("must specify password file")
	}
	rawPassword, err := passwordFromFile(c.String("password"))
	if err != nil {
		return nil, errors.Wrapf(err, "could not read password from file %s",
			c.String("password"))
	}
	return []byte(rawPassword), nil
}

// getPasswordAndKeyFile retrieves the password and key json from the files
// specified on the CL, or errors
func getPasswordAndKeyFile(c *clipkg.Context) (password []byte, keyjson []byte, err error) {
	password, err = getPassword(c)
	if err != nil {
		return nil, nil, err
	}
	if !c.IsSet("file") {
		return nil, nil, fmt.Errorf("must specify key file")
	}
	keypath := c.String("file")
	keyjson, err = ioutil.ReadFile(keypath)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to read file %s", keypath)
	}
	return password, keyjson, nil
}

// ImportVRFKey reads a file into an EncryptedSecretKey in the db
func (cli *Client) ImportVRFKey(c *clipkg.Context) error {
	password, keyjson, err := getPasswordAndKeyFile(c)
	if err != nil {
		return err
	}
	if err := vRFKeyStore(cli).Import(keyjson, string(password)); err != nil {
		if err == store.MatchingVRFKeyError {
			fmt.Println(`The database already has an entry for that public key.`)
			var key struct{ PublicKey string }
			if err := json.Unmarshal(keyjson, &key); err != nil {
				fmt.Println("could not extract public key from json input")
				return errors.Wrapf(err, "while extracting public key from %s", keyjson)
			}
			fmt.Println(`If you want to import the new key anyway, delete the old key with the command

` + "`chainlink local delete -pk " + key.PublicKey + "`" + `

(but maybe back it up first, with ` + "chainlink local export -pk <public_key> -f <backup_path>`)")
			return errors.Wrap(err, "while attempting to import key from CL")
		}
		return err
	}
	return nil
}

// ExportVRFKey saves encrypted copy of VRF key with given public key to
// requested file path. If there is more than one encrypted copy, the ones past
// the first are saved with extensions '.1', '.2', etc.
func (cli *Client) ExportVRFKey(c *clipkg.Context) error {
	enckeys, err := getKeys(cli, c)
	if err != nil {
		return err
	}
	if !c.IsSet("file") {
		return fmt.Errorf("must specify file to export to") // Or could default to stdout?
	}
	keypath := c.String("file")
	for i, keyjson := range enckeys {
		ckeypath := keypath
		if i > 0 {
			ckeypath = fmt.Sprintf("%s.%d", keypath, i)
		}
		if err := ioutil.WriteFile(ckeypath, keyjson, 0644); err != nil {
			return errors.Wrapf(err, "could not save %s to %s", keyjson, ckeypath)
		}
	}
	return nil
}

// getKeys retrieves the keys for an ExportVRFKey request
func getKeys(cli *Client, c *clipkg.Context) ([][]byte, error) {
	publicKey, err := getPublicKey(c)
	if err != nil {
		return nil, err
	}
	enckeys, err := vRFKeyStore(cli).Export(publicKey)
	if err != nil { // Tolerate errors here, in case some keys were retrievable
		logger.Infow("while retrieving keys with matching public key", publicKey, err)
	}
	return enckeys, nil
}

// DeleteVRFKey deletes the VRF key with given public key from the db
//
// Since this runs in an independent process from any chainlink node, it cannot
// cause other nodes to forget the key, if they already have it unlocked.
func (cli *Client) DeleteVRFKey(c *clipkg.Context) error {
	publicKey, err := getPublicKey(c)
	if err != nil {
		return err
	}
	if err := vRFKeyStore(cli).Delete(publicKey); err != nil {
		if err == store.AttemptToDeleteNonExistentKeyFromDB {
			fmt.Println("There is already no entry in the DB for " + publicKey.String())
		}
		return err
	}
	return nil
}

func getPublicKey(c *clipkg.Context) (*vrfkey.PublicKey, error) {
	if !c.IsSet("publicKey") {
		return nil, fmt.Errorf("must specify public key")
	}
	publicKey, err := vrfkey.NewPublicKeyFromHex(c.String("publicKey"))
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse public key")
	}
	return publicKey, nil
}

// ListKeys Lists the keys in the db
func (cli *Client) ListKeys(c *clipkg.Context) error {
	keys, err := vRFKeyStore(cli).ListKeys()
	if err != nil {
		return err
	}
	for _, key := range keys {
		fmt.Println(key)
	}
	logger.Infow("keys", "keys", keys)
	return nil
}

// Forget removes the key from the in-memory key store, but leaves it in the db
func (cli *Client) Forget(c *clipkg.Context) error {
	return nil
}
func noFileToOverwrite(path string) bool {
	return os.IsNotExist(utils.JustError(os.Stat(path)))
}
