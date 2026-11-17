package dill

import (
	"context"
)

type preflightStrictStub struct{}

func (preflightStrictStub) Up(context.Context, string) error                  { return nil }
func (preflightStrictStub) Plan(context.Context, string) (*PlanResult, error) { return nil, nil }
func (preflightStrictStub) Start(context.Context, string, bool) error         { return nil }
func (preflightStrictStub) Validate(context.Context, string, *string) error   { return nil }

func fmtGuardCase(strict, source, action bool) string {
	flag := func(b bool) string {
		if b {
			return "set"
		}
		return "nil"
	}
	return "strict-" + flag(strict) + "/source-" + flag(source) + "/action-" + flag(action)
}
