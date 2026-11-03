// Package version holds build information, set at build time with
// -ldflags "-X github.com/rhisijmuiden/iptv-portal/internal/version.Version=v1.2.3".
package version

// Version is the release version of this build.
var Version = "dev"

// License is the SPDX identifier of the license IPTV Portal is released under.
const License = "AGPL-3.0-only"

// Source is where the source code of IPTV Portal is published. The AGPL asks
// that users who reach the portal over a network are offered the source, so
// the admin panel and the account portal link here.
const Source = "https://github.com/rhisijmuiden/iptv-portal"

// LicenseURL points at the full license text in the source repository.
const LicenseURL = Source + "/blob/main/LICENSE"
