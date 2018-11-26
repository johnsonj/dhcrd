package dhcp

import (
	dhcp "github.com/krolaw/dhcp4"
	"log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type DHCPController struct {
	m manager.Manager
}

func New(m manager.Manager) (*DHCPController, error) {
	return &DHCPController{m: m}, nil
}

func (h *DHCPController) ServeDHCP(p dhcp.Packet, msgType dhcp.MessageType, options dhcp.Options) (d dhcp.Packet) {
	switch msgType {
	case dhcp.Discover:
		log.Printf("[Discover] %v", p)
		return
	case dhcp.Request:
		log.Printf("[Request] %v", p)
		return
	case dhcp.Release:
		log.Printf("[Release] %v", p)
		return
	case dhcp.Decline:
		log.Printf("[Decline] %v", p)
		return
	}

	return
}
