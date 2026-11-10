// Package mapper converts Inzhur transactions to Ghostfolio activities.
//
// The conversion is deterministic: the same input always gives the same
// output. Ghostfolio uses this to skip activities that it already has.
package mapper
