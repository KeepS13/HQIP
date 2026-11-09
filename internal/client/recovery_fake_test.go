package client

import "context"

func (f *fakeEngine) VerifyCurrent(context.Context, string) error { return nil }
func (f *fakeEngine) SetSuspended(context.Context, bool) error    { return nil }
