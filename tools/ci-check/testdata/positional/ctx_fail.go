// ctx_fail.go verifies that context exemption still enforces the cap on
// the remaining positional parameters.
package fixture

import "context"

func CtxPlusTwo(ctx context.Context, x int, y int) {}
