// client_test.go
package client

import (
	"fmt"
	"testing"
	"time"
)

func TestClient(t *testing.T) {
	c := NewClient("127.0.0.1:33889", "foo", "bar", TC_RDP, nil)
	err := c.Login()
	if err != nil {
		fmt.Println("Login:", err)
	}
	c.OnReady(func() {
		fmt.Println("ready")
	})
	time.Sleep(100 * time.Second)
}
