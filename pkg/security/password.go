// Copyright 2015 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package security

import (
	"context"
	"fmt"

	"github.com/cockroachdb/cockroach/pkg/clusterversion"
	"github.com/cockroachdb/cockroach/pkg/security/password"
	"github.com/cockroachdb/cockroach/pkg/settings"
	"github.com/cockroachdb/errors"
	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the cost to use when hashing passwords.
// It is exposed for testing.
//
// The default value of BcryptCost should increase along with
// computation power.
//
// For estimates, see:
// http://security.stackexchange.com/questions/17207/recommended-of-rounds-for-bcrypt
var BcryptCost = settings.RegisterIntSetting(
	settings.TenantWritable,
	BcryptCostSettingName,
	fmt.Sprintf(
		"the hashing cost to use when storing passwords supplied as cleartext by SQL clients "+
			"with the hashing method crdb-bcrypt (allowed range: %d-%d)",
		bcrypt.MinCost, bcrypt.MaxCost),
	// The default value 10 is equal to bcrypt.DefaultCost.
	// It incurs a password check latency of ~60ms on AMD 3950X 3.7GHz.
	// For reference, value 11 incurs ~110ms latency on the same hw, value 12 incurs ~390ms.
	password.DefaultBcryptCost,
	func(i int64) error {
		if i < int64(bcrypt.MinCost) || i > int64(bcrypt.MaxCost) {
			return bcrypt.InvalidCostError(int(i))
		}
		return nil
	}).WithPublic()

// BcryptCostSettingName is the name of the cluster setting BcryptCost.
const BcryptCostSettingName = "server.user_login.password_hashes.default_cost.crdb_bcrypt"

// SCRAMCost is the cost to use in SCRAM exchanges.
// The value of 4096 is the minimum value recommended by RFC 5802.
// It should be increased along with computation power.
var SCRAMCost = settings.RegisterIntSetting(
	settings.TenantWritable,
	SCRAMCostSettingName,
	fmt.Sprintf(
		"the hashing cost to use when storing passwords supplied as cleartext by SQL clients "+
			"with the hashing method scram-sha-256 (allowed range: %d-%d)",
		password.ScramMinCost, password.ScramMaxCost),
	// The minimum value 4096 incurs a password check latency of ~2ms on AMD 3950X 3.7GHz.
	//
	// The default value 119680 incurs ~60ms latency on the same hw.
	// This default was calibrated to incur a similar check latency as the
	// default value for BCryptCost above.
	// For further discussion, see the explanation on bcryptCostToSCRAMIterCount
	// below.
	//
	// For reference, value 250000 incurs ~125ms latency on the same hw,
	// value 1000000 incurs ~500ms.
	password.DefaultSCRAMCost,
	func(i int64) error {
		if i < password.ScramMinCost || i > password.ScramMaxCost {
			return errors.Newf("cost not in allowed range (%d,%d)", password.ScramMinCost, password.ScramMaxCost)
		}
		return nil
	}).WithPublic()

// SCRAMCostSettingName is the name of the cluster setting SCRAMCost.
const SCRAMCostSettingName = "server.user_login.password_hashes.default_cost.scram_sha_256"

// ErrEmptyPassword indicates that an empty password was attempted to be set.
var ErrEmptyPassword = errors.New("empty passwords are not permitted")

// ErrPasswordTooShort indicates that a client provided a password
// that was too short according to policy.
var ErrPasswordTooShort = errors.New("password too short")

// ErrUnknownHashMethod is returned by LoadPasswordHash if the hash encoding
// method is not supported.
var ErrUnknownHashMethod = errors.New("unknown hash method")

// PasswordHashMethod is the cluster setting that configures which
// hash method to use when clients request to store a cleartext password.
//
// It is exported for use in tests. Do not use this setting directly
// to read the current hash method. Instead use the
// GetConfiguredHashMethod() function.
var PasswordHashMethod = settings.RegisterEnumSetting(
	settings.TenantWritable,
	"server.user_login.password_encryption",
	"which hash method to use to encode cleartext passwords passed via ALTER/CREATE USER/ROLE WITH PASSWORD",
	// Note: the default is initially SCRAM, even in mixed-version clusters where
	// previous-version nodes do not know anything about SCRAM. This is handled
	// in the GetConfiguredPasswordHashMethod() function.
	"scram-sha-256",
	map[int64]string{
		int64(password.HashBCrypt):      password.HashBCrypt.String(),
		int64(password.HashSCRAMSHA256): password.HashSCRAMSHA256.String(),
	},
).WithPublic()

// hasClusterVersion verifies that all nodes have been upgraded to
// support the given target version key.
func hasClusterVersion(
	ctx context.Context, values *settings.Values, versionkey clusterversion.Key,
) bool {
	var vh clusterversion.Handle
	if values != nil {
		vh = values.Opaque().(clusterversion.Handle)
	}
	return vh != nil && vh.IsActive(ctx, versionkey)
}

// GetConfiguredPasswordHashMethod returns the configured hash method
// to use before storing passwords provided in cleartext from clients.
func GetConfiguredPasswordHashMethod(
	ctx context.Context, sv *settings.Values,
) (method password.HashMethod) {
	method = password.HashMethod(PasswordHashMethod.Get(sv))
	if method == password.HashSCRAMSHA256 && !hasClusterVersion(ctx, sv, clusterversion.SCRAMAuthentication) {
		// Not all nodes are upgraded to understand SCRAM yet. Force
		// Bcrypt for now, otherwise previous-version nodes will get confused.
		method = password.HashBCrypt
	}
	return method
}

// AutoDetectPasswordHashes is the cluster setting that configures whether
// the server recognizes pre-hashed passwords.
var AutoDetectPasswordHashes = settings.RegisterBoolSetting(
	settings.TenantWritable,
	"server.user_login.store_client_pre_hashed_passwords.enabled",
	"whether the server accepts to store passwords pre-hashed by clients",
	true,
)

// MinPasswordLength is the cluster setting that configures the
// minimum SQL password length.
var MinPasswordLength = settings.RegisterIntSetting(
	settings.TenantWritable,
	"server.user_login.min_password_length",
	"the minimum length accepted for passwords set in cleartext via SQL. "+
		"Note that a value lower than 1 is ignored: passwords cannot be empty in any case.",
	1,
	settings.NonNegativeInt,
).WithPublic()

// AutoUpgradePasswordHashes is the cluster setting that configures whether to
// automatically re-encode stored passwords using crdb-bcrypt to scram-sha-256.
var AutoUpgradePasswordHashes = settings.RegisterBoolSetting(
	settings.TenantWritable,
	"server.user_login.upgrade_bcrypt_stored_passwords_to_scram.enabled",
	"whether to automatically re-encode stored passwords using crdb-bcrypt to scram-sha-256",
	true,
).WithPublic()
