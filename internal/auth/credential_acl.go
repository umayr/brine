package auth

// CredentialACLSafe applies the existing credential ACL policy to an already
// open descriptor. This does not establish trusted ancestry or file ownership.
func CredentialACLSafe(fd int) bool { return credentialACLSafe(fd) }
