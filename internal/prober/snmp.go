package prober

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/Faqihyugos/monstack-go/pkg/models"
)

// SNMPProber performs native Go SNMP v2c/v3 queries and walks without an external exporter.
type SNMPProber struct{}

// NewSNMPProber creates a new SNMPProber.
func NewSNMPProber() *SNMPProber {
	return &SNMPProber{}
}

// Probe queries configured OIDs (or sysUpTime by default) and optional walk subtrees via SNMP.
func (p *SNMPProber) Probe(ctx context.Context, target *models.Target) (*models.ProbeResult, error) {
	now := time.Now().UTC()
	res := &models.ProbeResult{
		TargetID:   target.ID,
		TargetName: target.Name,
		TargetType: target.Type,
		Timestamp:  now,
		Metadata:   make(map[string]any),
	}

	timeout := 5 * time.Second
	if target.TimeoutMs > 0 {
		timeout = time.Duration(target.TimeoutMs) * time.Millisecond
	}

	host := target.Target
	port := uint16(161)
	if h, pStr, err := net.SplitHostPort(target.Target); err == nil {
		host = h
		if pn, err := strconv.Atoi(pStr); err == nil && pn > 0 && pn <= 65535 {
			port = uint16(pn)
		}
	}
	if v, ok := target.Config["port"]; ok {
		switch pv := v.(type) {
		case int:
			if pv > 0 && pv <= 65535 {
				port = uint16(pv)
			}
		case uint16:
			if pv > 0 {
				port = pv
			}
		case float64:
			if pv > 0 && pv <= 65535 {
				port = uint16(pv)
			}
		}
	}

	community := getConfigString(target.Config, "community")
	if community == "" {
		community = "public"
	}

	version := gosnmp.Version2c
	switch strings.ToLower(strings.TrimSpace(getConfigString(target.Config, "version"))) {
	case "1", "v1":
		version = gosnmp.Version1
	case "3", "v3":
		version = gosnmp.Version3
	}

	client := &gosnmp.GoSNMP{
		Target:    host,
		Port:      port,
		Community: community,
		Version:   version,
		Timeout:   timeout,
		Retries:   1,
		Context:   ctx,
	}

	start := time.Now()
	if err := client.Connect(); err != nil {
		res.ResponseTime = time.Since(start)
		res.Success = false
		res.Error = fmt.Sprintf("snmp connect failed: %v", err)
		return res, nil
	}
	defer client.Conn.Close()

	oidNames := make(map[string]string)
	var getOIDs []string

	if rawOIDs, ok := target.Config["oids"].([]any); ok {
		for _, item := range rawOIDs {
			if m, ok := item.(map[string]any); ok {
				oid := getConfigString(m, "oid")
				name := getConfigString(m, "name")
				if oid != "" {
					if !strings.HasPrefix(oid, ".") {
						oid = "." + oid
					}
					getOIDs = append(getOIDs, oid)
					if name != "" {
						oidNames[oid] = name
					}
				}
			}
		}
	}

	var walkOIDs []string
	if rawWalk, ok := target.Config["walk"].([]any); ok {
		for _, item := range rawWalk {
			if s, ok := item.(string); ok && s != "" {
				walkOIDs = append(walkOIDs, s)
			}
		}
	} else if rawWalkStr, ok := target.Config["walk"].([]string); ok {
		walkOIDs = append(walkOIDs, rawWalkStr...)
	}

	// Default to sysUpTime if no OIDs or walk subtrees were specified
	if len(getOIDs) == 0 && len(walkOIDs) == 0 {
		defaultOID := ".1.3.6.1.2.1.1.3.0"
		getOIDs = []string{defaultOID}
		oidNames[defaultOID] = "sysUpTime"
	}

	oidCount := 0
	if len(getOIDs) > 0 {
		pkt, err := client.Get(getOIDs)
		if err != nil {
			res.ResponseTime = time.Since(start)
			res.Success = false
			res.Error = fmt.Sprintf("snmp get failed: %v", err)
			return res, nil
		}
		for _, pdu := range pkt.Variables {
			oidCount++
			if val, ok := snmpPDUToFloat(pdu); ok {
				metricName := "probe_snmp_" + sanitizeOIDMetric(pdu.Name, oidNames)
				res.Metrics = append(res.Metrics, models.MetricPoint{
					TargetID:  target.ID,
					Metric:    metricName,
					Value:     val,
					Timestamp: now,
					Labels:    map[string]string{"oid": pdu.Name},
				})
			}
		}
	}

	for _, rootOID := range walkOIDs {
		err := client.Walk(rootOID, func(pdu gosnmp.SnmpPDU) error {
			oidCount++
			if val, ok := snmpPDUToFloat(pdu); ok {
				metricName := "probe_snmp_" + sanitizeOIDMetric(pdu.Name, oidNames)
				res.Metrics = append(res.Metrics, models.MetricPoint{
					TargetID:  target.ID,
					Metric:    metricName,
					Value:     val,
					Timestamp: now,
					Labels:    map[string]string{"oid": pdu.Name},
				})
			}
			return nil
		})
		if err != nil {
			res.ResponseTime = time.Since(start)
			res.Success = false
			res.Error = fmt.Sprintf("snmp walk %s failed: %v", rootOID, err)
			return res, nil
		}
	}

	elapsed := time.Since(start)
	res.ResponseTime = elapsed
	res.Metrics = append(res.Metrics,
		models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricSNMPScrapeSeconds,
			Value:     elapsed.Seconds(),
			Timestamp: now,
		},
		models.MetricPoint{
			TargetID:  target.ID,
			Metric:    models.MetricSNMPOIDCount,
			Value:     float64(oidCount),
			Timestamp: now,
		},
	)
	res.Metadata["oid_count"] = oidCount
	res.Success = true
	return res, nil
}

func snmpPDUToFloat(pdu gosnmp.SnmpPDU) (float64, bool) {
	switch pdu.Type {
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Counter64, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Uinteger32:
		return float64(gosnmp.ToBigInt(pdu.Value).Int64()), true
	default:
		return 0, false
	}
}

func sanitizeOIDMetric(oid string, names map[string]string) string {
	if name, ok := names[oid]; ok && name != "" {
		return strings.ToLower(name)
	}
	trimmed := strings.TrimPrefix(oid, ".")
	return strings.ReplaceAll(trimmed, ".", "_")
}
