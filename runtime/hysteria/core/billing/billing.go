// Package billing retains the HY2 import path for existing consumers. Business
// types and fixed-point arithmetic are owned by the independent nodepolicy module.
package billing

import shared "github.com/KeepS13/HQIP/runtime/nodepolicy/billing"

const Capability = shared.Capability
const MaxRawBytes = shared.MaxRawBytes

type Policy = shared.Policy
type Grant = shared.Grant
type Segment = shared.Segment

func Units(raw, multiplier int64) (int64, error) { return shared.Units(raw, multiplier) }
func Add(left, right int64) (int64, error)       { return shared.Add(left, right) }
func Ceiling(units int64) int64                  { return shared.Ceiling(units) }
