/**
 * Copyright 2015 Comcast Cable Communications Management, LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package jtl

import (
	"encoding/json"

	. "github.com/Comcast/eel/eel/util"
)

// handleEvent processes an event (usually from the work queue) by selecting the correct handlers, applying the appropriate transformations and then sending off the tranformed event via appropriate publisher(s).
func handleEvent(ctx Context, stats *ServiceStats, event *JDoc, raw string, debug bool, sync bool) interface{} {
	debuginfo := make([]interface{}, 0)
	ctx.AddLogValue("destination", "unknown")
	handlers := GetHandlerFactory(ctx).GetHandlersForEvent(ctx, event)
	if len(handlers) == 0 {
		ctx.Log().Info("event", "no_matching_handlers", "trace.in.data", raw)
	}
	initialCtx := ctx
	ctx = ctx.SubContext()
	for _, handler := range handlers {
		//TODO: validate JSON schema
		publishers, err := handler.ProcessEvent(initialCtx.SubContext(), event)
		if err != nil {
			ctx.Log().Error("event", "bad_transformation", "handler", handler.Name, "tenant", handler.TenantId, "trace.in.data", raw, "error", err.Error())
			ctx.Log().Metric("bad_transformation", M_Namespace, "xrs", M_Metric, "bad_transformation", M_Unit, "Count", M_Dims, "app="+AppId+"&env="+EnvName+"&instance="+InstanceName+"&destination="+ctx.LogValue("destination").(string), M_Val, 1.0)
			stats.IncErrors()
			continue
		}
		if len(publishers) == 0 {
			// no publisher, likely due to filtering
			ctx.Log().Info("event", "filtered_event", "tenant", handler.TenantId, "handler", handler.Name)
			continue
		}
		for _, publisher := range publishers {
			dc := ctx.Value(EelDuplicateChecker).(DuplicateChecker)
			if dc.GetTtl() > 0 && dc.IsDuplicate(ctx, []byte(publisher.GetUrl()+"\n"+publisher.GetPayload())) {
				ctx.Log().Error("status", "200", "event", "dropping_duplicate", "handler", handler.Name, "tenant", handler.TenantId, "trace.in.data", raw)
				ctx.Log().Metric("dropping_duplicate", M_Namespace, "xrs", M_Metric, "dropping_duplicate", M_Unit, "Count", M_Dims, "app="+AppId+"&env="+EnvName+"&instance="+InstanceName+"&destination="+ctx.LogValue("destination").(string), M_Val, 1.0)
				continue
			}
			// trace header
			traceHeaderKey := GetConfig(ctx).HttpTransactionHeader
			if publisher.GetHeaders() == nil {
				publisher.SetHeaders(make(map[string]string, 0))
			}
			if publisher.GetHeaders()[traceHeaderKey] == "" && ctx.LogValue("tx.traceId") != nil {
				publisher.GetHeaders()[traceHeaderKey] = ctx.LogValue("tx.traceId").(string)
			}
			ctx.AddLogValue("tx.traceId", publisher.GetHeaders()[traceHeaderKey])
			// other log params
			ctx.AddLogValue("trace.out.url", publisher.GetUrl())
			ctx.AddLogValue("topic", handler.Topic)
			ctx.AddLogValue("tenant", handler.TenantId)
			ctx.AddLogValue("handler", handler.Name)
			ctx.AddLogValue("trace.in.data", raw)
			ctx.AddLogValue("trace.out.data", publisher.GetPayload())
			ctx.AddLogValue("trace.out.protocol", publisher.GetProtocol())
			ctx.AddLogValue("trace.out.path", publisher.GetPath())
			ctx.AddLogValue("trace.out.headers", publisher.GetHeaders())
			ctx.AddLogValue("trace.out.protocol", publisher.GetProtocol())
			ctx.AddLogValue("trace.out.endpoint", publisher.GetEndpoint())
			ctx.AddLogValue("trace.out.verb", publisher.GetVerb())
			ctx.AddLogValue("trace.out.url", publisher.GetUrl())
			if sync {
				// no need to call out to endpoint in sync mode
				debuginfo = append(debuginfo, publisher.GetPayloadParsed().GetOriginalObject())
			} else if debug {
				// sequential execution to collect debug info
				_, err := publisher.Publish()
				ctx.AddLogValue("trace.out.endpoint", publisher.GetEndpoint())
				ctx.AddLogValue("trace.out.url", publisher.GetUrl())
				if err != nil {
					ctx.Log().Error("event", "publish_failed", "error", err.Error())
					ctx.Log().Metric("publish_failed", M_Namespace, "xrs", M_Metric, "publish_failed", M_Unit, "Count", M_Dims, "app="+AppId+"&env="+EnvName+"&instance="+InstanceName+"&destination="+ctx.LogValue("destination").(string), M_Val, 1.0)
					stats.IncErrors()
				} else {
					ctx.Log().Info("event", "published_event")
					ctx.Log().Metric("published_event", M_Namespace, "xrs", M_Metric, "published_event", M_Unit, "Count", M_Dims, "app="+AppId+"&env="+EnvName+"&instance="+InstanceName+"&destination="+ctx.LogValue("destination").(string), M_Val, 1.0)
					stats.IncOutCount()
				}
				de := make(map[string]interface{}, 0)
				de["trace.out.endpoint"] = publisher.GetEndpoint()
				de["trace.out.path"] = publisher.GetPath()
				de["trace.out.headers"] = publisher.GetHeaders()
				de["trace.out.protocol"] = publisher.GetProtocol()
				de["trace.out.verb"] = publisher.GetVerb()
				de["trace.out.url"] = publisher.GetUrl()
				if publisher.GetPayload() != "" {
					data := make(map[string]interface{})
					err := json.Unmarshal([]byte(publisher.GetPayload()), &data)
					if err == nil {
						de["trace.out.data"] = data
					} else {
						de["trace.out.data"] = publisher.GetPayload()
					}
				} else {
					de["trace.out.data"] = ""
				}
				de["trace.in.data"] = event.GetOriginalObject()
				de["tenant.id"] = handler.TenantId
				de["handler"] = handler.Name
				de["api"] = publisher.GetApi()
				de["tx.id"] = ctx.Id()
				de["tx.traceId"] = publisher.GetHeaders()[traceHeaderKey]
				debuginfo = append(debuginfo, de)

			} else {
				//c := ctx
				//p := publisher
				go func(c Context, p EventPublisher) {
					_, err := p.Publish()
					c.AddLogValue("trace.out.endpoint", p.GetEndpoint())
					c.AddLogValue("trace.out.url", p.GetUrl())
					if err != nil {
						c.Log().Error("event", "publish_failed", "error", err.Error())
						c.Log().Metric("publish_failed", M_Namespace, "xrs", M_Metric, "publish_failed", M_Unit, "Count", M_Dims, "app="+AppId+"&env="+EnvName+"&instance="+InstanceName+"&destination="+ctx.LogValue("destination").(string), M_Val, 1.0)
						stats.IncErrors()
					} else {
						c.Log().Info("event", "published_event")
						c.Log().Metric("published_event", M_Namespace, "xrs", M_Metric, "published_event", M_Unit, "Count", M_Dims, "app="+AppId+"&env="+EnvName+"&instance="+InstanceName+"&destination="+ctx.LogValue("destination").(string), M_Val, 1.0)
						stats.IncOutCount()
					}
				}(ctx.SubContext(), publisher)
			}
		}
	}
	return debuginfo
}