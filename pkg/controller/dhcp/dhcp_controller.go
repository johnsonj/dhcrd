package dhcp

import (
	"context"
	"encoding/binary"
	"fmt"
	api "github.com/johnsonj/dhcrd/pkg/apis/dhcp/v1alpha1"
	dhcp "github.com/krolaw/dhcp4"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log"
	"math/big"
	"net"
	"os"
	clientapi "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"strings"
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
	podIp := os.Getenv("POD_IP")
	if podIp == "" {
		return nil, fmt.Errorf("empty POD_IP env variable")
	}

	return &DHCPController{
		m:      m,
		client: m.GetClient(),
		server: net.ParseIP(podIp).To4(),
		// TODO: these are hardcoded for my network
		namespace:     "network",
		leaseDuration: 8 * time.Hour,
	}, nil
}

func optionsToString(options dhcp.Options) string {
	opts := ""
	for optCode, val := range options {
		switch optCode {
		case dhcp.OptionVendorClassIdentifier, dhcp.OptionHostName, dhcp.OptionDomainName:
			opts += fmt.Sprintf("{ %s: %s }", optCode, string(val))
		case dhcp.OptionClientIdentifier:
			opts += fmt.Sprintf("{ %s: 0x%x }", optCode, val)
		case dhcp.OptionRequestedIPAddress, dhcp.OptionServerIdentifier, dhcp.OptionRouter,
			dhcp.OptionDomainNameServer, dhcp.OptionSubnetMask, dhcp.OptionBroadcastAddress:
			opts += fmt.Sprintf("{ %s: %s }", optCode, net.IP(val))
		case dhcp.OptionIPAddressLeaseTime, dhcp.OptionRenewalTimeValue, dhcp.OptionRebindingTimeValue:
			opts += fmt.Sprintf("{ %s: %s }", optCode, time.Duration(binary.BigEndian.Uint32(val))*time.Second)
		case dhcp.OptionParameterRequestList:
			var req []string
			for _, optCode := range val {
				req = append(req, dhcp.OptionCode(optCode).String())
			}
			opts += fmt.Sprintf("{ %s: %s", optCode, strings.Join(req, ","))
		default:
			opts += fmt.Sprintf("{ %s }", optCode)
		}
	}

	return opts
}

func (h *DHCPController) ServeDHCP(p dhcp.Packet, msgType dhcp.MessageType, options dhcp.Options) (d dhcp.Packet) {
	ctx := context.Background()
	hostname := ""
	for optCode, val := range options {
		switch optCode {
		case dhcp.OptionHostName:
			hostname = string(val)
		}
	}
	log.Printf("[%s] packet recieved, options: %s", msgType.String(), optionsToString(options))

	switch msgType {
	case dhcp.Discover:
		ip, r, err := h.nextIp()
		if err != nil {
			log.Printf("[Discover] error finding next ip: %v", err)
			return
		}
		if r == nil || ip == nil {
			log.Printf("[Discover] unexpected nil range or ip")
			return
		}
		opts := h.buildOptions(*ip)
		log.Printf("[Discover] offering ip (%v), opts: %s", ip.String(), optionsToString(opts))
		return dhcp.ReplyPacket(p, dhcp.Offer, h.server, *ip, h.leaseDuration, opts.SelectOrderOrAll(options[dhcp.OptionParameterRequestList]))

	case dhcp.Request:
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
				lease.Spec.Hostname = hostname
				err := h.client.Update(ctx, &lease)
				if err != nil {
					log.Printf("[Request] failed to update lease(%#v): %v", lease, err)
					return dhcp.ReplyPacket(p, dhcp.NAK, h.server, nil, 0, nil)
				}
				log.Printf("[Request] request granted, sending ACK")
				opts := h.buildOptions(ip)
				return dhcp.ReplyPacket(p, dhcp.ACK, h.server, ip, h.leaseDuration, opts.SelectOrderOrAll(options[dhcp.OptionParameterRequestList]))
			} else {
				log.Printf("[Request] request is a for an already leased IP, sending NAK")
				return dhcp.ReplyPacket(p, dhcp.NAK, h.server, nil, 0, nil)
			}
		} else if errors.IsNotFound(err) {
			log.Printf("[Request] request is for an unleased IP")
			// TODO: Validate the requested IP, find its associated range, check if we allocate it
			lease := api.Lease{
				ObjectMeta: metav1.ObjectMeta{Name: ip.String(), Namespace: h.namespace},
				Spec: api.LeaseSpec{
					Mac:        p.CHAddr().String(),
					Expiration: time.Now().Add(h.leaseDuration).Format(time.RFC3339),
					Hostname:   hostname,
				},
			}
			err := h.client.Create(ctx, &lease)
			if err != nil {
				log.Printf("[Request] failed to create the lease(%#v): %v", lease, err)
				return dhcp.ReplyPacket(p, dhcp.NAK, h.server, nil, 0, nil)
			}
			log.Printf("[Request] created lease (%s) (%#v)", lease.Name, lease)

			opts := h.buildOptions(ip)
			log.Printf("[Request] request granted (%s), sending ACK, opts: %s", ip, optionsToString(opts))
			return dhcp.ReplyPacket(p, dhcp.ACK, h.server, ip, h.leaseDuration, opts.SelectOrderOrAll(options[dhcp.OptionParameterRequestList]))
		} else {
			log.Printf("[Request] failed to query for lease: %v", err)
		}
		return dhcp.ReplyPacket(p, dhcp.NAK, h.server, nil, 0, nil)
	case dhcp.Release, dhcp.Decline:
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

func toByteArr(v uint32) []byte {
	bs := make([]byte, 4)
	binary.BigEndian.PutUint32(bs, v)
	return bs
}

func (h *DHCPController) buildOptions(ip net.IP) dhcp.Options {
	// TODO/HACK: this assumes the nextIp will be in the proper range
	// race condition waiting to happen!
	// Can we get this info from the client's request instead?
	_, r, _ := h.nextIp()
	opts := dhcp.Options{
		dhcp.OptionSubnetMask:         []byte{255, 255, 0, 0},
		dhcp.OptionRouter:             []byte(net.ParseIP(r.Spec.Router).To4()),
		dhcp.OptionDomainNameServer:   []byte(net.ParseIP(r.Spec.DNS).To4()),
		dhcp.OptionIPAddressLeaseTime: toByteArr(uint32(h.leaseDuration.Seconds())),
		dhcp.OptionRenewalTimeValue:   toByteArr(uint32(h.leaseDuration.Seconds() / 2)),
		dhcp.OptionRebindingTimeValue: toByteArr(uint32((h.leaseDuration.Seconds() / 4) + (h.leaseDuration.Seconds() / 2))),
		// TODO: make this configurable
		dhcp.OptionBroadcastAddress: []byte{10, 10, 255, 255},
		dhcp.OptionDomainName:       []byte("int.cd.sea.johnsonjeff.com"),
	}

	return opts
}

func nextIP(ip net.IP) net.IP {
	// Convert to big.Int and increment
	ipb := big.NewInt(0).SetBytes([]byte(ip))
	ipb.Add(ipb, big.NewInt(1))

	// Add leading zeros
	b := ipb.Bytes()
	b = append(make([]byte, len(ip)-len(b)), b...)
	return net.IP(b)
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
			ip = nextIP(ip.To4())

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
