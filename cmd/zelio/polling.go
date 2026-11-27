package main

import (
	"context"

	zelio "github.com/MaxXxaM/zelio-usb-go"
)

// monitoringRefreshClient adds the read-only status and FBD queries between
// display reads. The front-panel snapshot remains the only source of state.
type monitoringRefreshClient struct {
	*zelio.Client
}

func (c monitoringRefreshClient) ReadMonitoringIO(ctx context.Context) (zelio.MonitoringIO, error) {
	snapshot, err := c.Client.ReadMonitoringIO(ctx)
	if err != nil {
		return zelio.MonitoringIO{}, err
	}
	if _, err := c.ReadMonitoringRawFBD(ctx); err != nil {
		return zelio.MonitoringIO{}, err
	}
	if _, err := c.ReadMonitoringStatus(ctx); err != nil {
		return zelio.MonitoringIO{}, err
	}
	return snapshot, nil
}
