package light

// SetDiscover makes the controller discover lights with fn.
func (c *Controller) SetDiscover(fn func() (*Yeelight, error)) { c.discover = fn }
