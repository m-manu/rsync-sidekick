package remote

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"
)

// DialSSH establishes an SSH connection to the given location.
//
// Auth methods tried in order:
//  1. SSH agent (if SSH_AUTH_SOCK is set)
//  2. Key files (~/.ssh/id_ed25519, id_rsa, id_ecdsa) or explicitKeyPath
//  3. Interactive password prompt
func DialSSH(loc Location, explicitKeyPath string) (*ssh.Client, error) {
	var authMethods []ssh.AuthMethod

	// 1. SSH agent
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	// 2. Key files
	keyPaths := defaultKeyPaths()
	if explicitKeyPath != "" {
		keyPaths = []string{explicitKeyPath}
	}
	for _, kp := range keyPaths {
		if signer := loadKey(kp); signer != nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		}
	}

	// 3. Password prompt (interactive)
	authMethods = append(authMethods, ssh.PasswordCallback(func() (string, error) {
		fmt.Fprintf(os.Stderr, "Password for %s: ", loc.SSHSpec())
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(pw), nil
	}))

	// Known hosts
	hostKeyCallback := ssh.InsecureIgnoreHostKey()
	knownHostsPath := filepath.Join(userHomeDir(), ".ssh", "known_hosts")
	if cb, err := knownhosts.New(knownHostsPath); err == nil {
		hostKeyCallback = cb
	}

	user := loc.User
	if user == "" {
		user = currentUser()
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
	}

	client, err := ssh.Dial("tcp", loc.SSHAddr(), config)
	if err != nil {
		return nil, fmt.Errorf("SSH connection to %s failed: %w", loc.SSHSpec(), err)
	}
	return client, nil
}

func loadKey(path string) ssh.Signer {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		// Try with passphrase
		fmt.Fprintf(os.Stderr, "Passphrase for key %s: ", path)
		pw, pwErr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if pwErr != nil {
			return nil
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, pw)
		if err != nil {
			return nil
		}
	}
	return signer
}

func defaultKeyPaths() []string {
	home := userHomeDir()
	return []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".ssh", "id_rsa"),
		filepath.Join(home, ".ssh", "id_ecdsa"),
	}
}

func userHomeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "root"
}
