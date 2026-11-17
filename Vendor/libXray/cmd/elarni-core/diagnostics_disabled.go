//go:build !elarni_diagnostics

package main

// diagnosticsBuild marks the build that records everything; it never replaces itself with a
// release, which would end the recording its user was asked to make.
const diagnosticsBuild = false

func diagnosticsMain() (bool, int) { return false, 0 }
