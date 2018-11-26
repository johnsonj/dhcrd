package dhcp

import (
	"context"
	"fmt"
	api "github.com/johnsonj/dhcrd/pkg/apis/dhcp/v1alpha1"
	dhcp "github.com/krolaw/dhcp4"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log"
	"net"
	clientapi "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"time"
)

type DHCPController struct {
	m             manager.Manager
	client        clientapi.Client
	server        net.IP
	leaseDuration time.Duration
	options       dhcp.Options
	namespace     string
}

func New(m manager.Manager) (*DHCPController, error) {
	return &DHCPController{
		m:      m,
		client: m.GetClient(),
		// TODO: these are hardcoded for my network
		server:        net.IP{10, 10, 0, 157},
		namespace:     "network",
		leaseDuration: 2 * time.Hour,
	}, nil
}

func (h *DHCPController) ServeDHCP(p dhcp.Packet, msgType dhcp.MessageType, options dhcp.Options) (d dhcp.Packet) {
	ctx := context.Background()

	switch msgType {
	case dhcp.Discover:
		log.Printf("[Discover] packet recieved")
		ip, r, err := h.nextIp()
		if err != nil {
			log.Printf("[Discover] error finding next ip: %v", err)
			return
		}
		if r == nil || ip == nil {
			log.Printf("[Discover] unexpected nil range or ip")
			return
		}
		log.Printf("[Discover] offering ip (%v)", ip.String())
		opts := dhcp.Options{
			dhcp.OptionSubnetMask:       []byte{255, 255, 0, 0},
			dhcp.OptionRouter:           []byte(net.ParseIP(r.Spec.Router)),
			dhcp.OptionDomainNameServer: []byte(net.ParseIP(r.Spec.DNS)),
		}
		return dhcp.ReplyPacket(p, dhcp.Offer, h.server, *ip, h.leaseDuration, opts.SelectOrderOrAll(options[dhcp.OptionParameterRequestList]))

	case dhcp.Request:
		log.Printf("[Request] packet recieved")
		if server, ok := options[dhcp.OptionServerIdentifier]; ok && !net.IP(server).Equal(h.server) {
			log.Printf("[Request] request not for this server, not responding")
			return nil
		}

		ip := net.IP(options[dhcp.OptionRequestedIPAddress])
		if ip == nil {
			// A renew?
			ip = net.IP(p.CIAddr())
		}

		var lease api.Lease
		err := h.client.Get(ctx, clientapi.ObjectKey{Namespace: h.namespace, Name: ip.String()}, &lease)
		if err == nil {
			log.Printf("[Request] request is for an existing lease (%s) (%#v)", lease.Name, lease.Spec)
			if lease.Spec.Mac == p.CHAddr().String() {
				log.Printf("[Request] request is a renewal")
				// This IP is leased to this client, renew the lease
				lease.Spec.Expiration = time.Now().Add(h.leaseDuration).Format(time.RFC3339)
				err := h.client.Update(ctx, &lease)
				if err != nil {
					log.Printf("[Request] failed to update lease(%#v): %v", lease, err)
					return dhcp.ReplyPacket(p, dhcp.NAK, h.server, nil, 0, nil)
				}
				log.Printf("[Request] request granted, sending ACK")
				return dhcp.ReplyPacket(p, dhcp.ACK, h.server, ip, h.leaseDuration, options.SelectOrderOrAll(options[dhcp.OptionParameterRequestList]))
			}
		} else if errors.IsNotFound(err) {
			log.Printf("[Request] request is for a unleased IP")
			// TODO: Validate the requested IP, find its associated range, check if we allocate it
			lease := api.Lease{
				ObjectMeta: metav1.ObjectMeta{Name: ip.String(), Namespace: h.namespace},
				Spec: api.LeaseSpec{
					Mac:        p.CHAddr().String(),
					Expiration: time.Now().Add(h.leaseDuration).Format(time.RFC3339),
				},
			}
			err := h.client.Create(ctx, &lease)
			if err != nil {
				log.Printf("[Request] failed to create the lease(%#v): %v", lease, err)
				return dhcp.ReplyPacket(p, dhcp.NAK, h.server, nil, 0, nil)
			}
			log.Printf("[Request] created lease (%s) (%v)", lease.Name, lease.Spec)

			// TODO/HACK: this assumes the nextIp will be in the proper range
			// race condition waiting to happen!
			// Can we get this info from the client's request instead?
			_, r, _ := h.nextIp()
			opts := dhcp.Options{
				dhcp.OptionSubnetMask:       []byte{255, 255, 0, 0},
				dhcp.OptionRouter:           []byte(net.ParseIP(r.Spec.Router)),
				dhcp.OptionDomainNameServer: []byte(net.ParseIP(r.Spec.DNS)),
			}

			log.Printf("[Request] request granted, sending ACK")
			return dhcp.ReplyPacket(p, dhcp.ACK, h.server, ip, h.leaseDuration, opts.SelectOrderOrAll(options[dhcp.OptionParameterRequestList]))
		} else {
			log.Printf("[Request] failed to query for lease: %v", err)
		}
		return dhcp.ReplyPacket(p, dhcp.NAK, h.server, nil, 0, nil)
	case dhcp.Release, dhcp.Decline:
		log.Printf("[Release, Decline] packet recieved", p)
		mac := p.CHAddr().String()

		listOpts := &clientapi.ListOptions{Namespace: h.namespace}
		var list api.LeaseList
		err := h.client.List(ctx, listOpts, &list)
		if err != nil {
			log.Printf("[Release, Decline] searching for lease (client: %v): %v", mac, err)
			return
		}

		for _, l := range list.Items {
			if l.Spec.Mac == mac {
				err := h.client.Delete(ctx, &l)
				if err != nil {
					log.Printf("[Release, Decline] deleting lease (client: %v): %v", mac, err)
				}
				return
			}
		}
		return
	}

	return
}

func (h *DHCPController) nextIp() (*net.IP, *api.Range, error) {
	ctx := context.TODO()
	listOpts := &clientapi.ListOptions{Namespace: h.namespace}
	var list api.RangeList
	err := h.client.List(ctx, listOpts, &list)
	if err != nil {
		return nil, nil, fmt.Errorf("listing ranges: %v", err)
	}
	for _, r := range list.Items {
		// SUPER HACK:
		// Start from the beginning of the CIDR, look for an unused lease until we find it or run out
		ip, net, err := net.ParseCIDR(r.Spec.CIDR)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing cidr (%v): %v", r.Spec.CIDR, err)
		}
		for {
			ip = dhcp.IPAdd(ip, 1)
			var lease api.Lease
			err := h.client.Get(ctx, clientapi.ObjectKey{Namespace: h.namespace, Name: ip.String()}, &lease)
			if err == nil {
				// a lease for this IP exists, is it expired?
				t, err := time.Parse(time.RFC3339, lease.Spec.Expiration)
				if err != nil {
					fmt.Printf("error parsing time (%s): %v", lease.Spec.Expiration, err)
				}

				if t.Add(h.leaseDuration).After(time.Now()) {
					fmt.Printf("reclaiming expired lease (%s) from (%s)", lease.Name, lease.Spec.Expiration)

					err := h.client.Delete(ctx, &lease)
					if err != nil {
						log.Printf("reclaiming expired lease (%s) from (%s) failed: %v", lease.Name, lease.Spec.Expiration, err)
					} else {
						return &ip, &r, nil
					}
				}

				continue
			} else if errors.IsNotFound(err) {
				// this IP has not been leased
				break
			}
		}
		if !net.Contains(ip) {
			// no more IPs left in this range
			continue
		} else {
			return &ip, &r, nil
		}
	}

	return nil, nil, fmt.Errorf("no range with avaliable leases")
}
