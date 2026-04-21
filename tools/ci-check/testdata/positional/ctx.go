// ctx.go exercises the context.Context exemption for the first parameter.
package fixture

import "context"

func CtxOnly(ctx context.Context) {}

func CtxPlusOne(ctx context.Context, x int) {}
