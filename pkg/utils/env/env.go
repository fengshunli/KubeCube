/*
Copyright 2021 KubeCube Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package env

import (
	"net/http"
	"os"
	"sync"

	"github.com/kubecube-io/kubecube/pkg/clog"
	"github.com/kubecube-io/kubecube/pkg/utils/constants"
)

type AuditSvcApi struct {
	URL    string
	Method string
	Header string
}

func WardenImage() string {
	return os.Getenv("WARDEN_IMAGE")
}

func WardenInitImage() string {
	return os.Getenv("WARDEN_INIT_IMAGE")
}

func DependenceJobImage() string {
	return os.Getenv("DEPENDENCE_JOB_IMAGE")
}

func PivotCubeHost() string {
	return os.Getenv("PIVOT_CUBE_HOST")
}

func WardenRegisterModeEnable() string {
	v := os.Getenv("WARDEN_REGISTER_MODE_ENABLE")
	if v == "" {
		v = "false"
	}
	return v
}

func PivotCubeClusterIPSvc() string {
	r := os.Getenv("PIVOT_CUBE_CLUSTER_IP_SVC")
	if r == "" {
		r = constants.DefaultPivotCubeClusterIPSvc
	}
	return r
}

func AuditIsEnable() bool {
	r := os.Getenv("AUDIT_IS_ENABLE")
	if r == "false" {
		return false
	}
	return true
}

func AuditSVC() AuditSvcApi {
	r := os.Getenv("AUDIT_URL")
	h := os.Getenv("AUDIT_HEADER")
	if r == "" {
		r = constants.DefaultAuditURL
		h = "Content-Type=application/json;charset=UTF-8"
	}
	m := os.Getenv("AUDIT_METHOD")
	if m == "" {
		m = http.MethodPost
	}
	return AuditSvcApi{r, m, h}
}

func AuditEventSource() string {
	r := os.Getenv("AUDIT_EVENT_SOURCE")
	if r == "" {
		r = "KubeCube"
	}
	return r
}

func JwtSecret() string {
	return os.Getenv("JWT_SECRET")
}

func ChartsDownload() string {
	r := os.Getenv("DOWNLOAD_CHARTS")
	if r == "" {
		r = "true"
	}
	return r
}

func ChartsDownloadUrl() string {
	return os.Getenv("DOWNLOAD_CHARTS_URL")
}

func AuditLanguage() string {
	l := os.Getenv("AUDIT_LANGUAGE")
	if l == "" {
		l = "en"
	}
	return l
}

func RetainMemberClusterResource() bool {
	res := os.Getenv("RETAIN_MEMBER_CLUSTER_RESOURCE")
	if res == "true" {
		return true
	}

	return false
}

var (
	once          sync.Once
	cubeNamespace = "kubecube-system"
)

func CubeNamespace() string {
	once.Do(func() {
		ns, ok := os.LookupEnv("CUBE_NAMESPACE")
		if ok {
			cubeNamespace = ns
		}
		clog.Info("kubecube running in namespace %v", cubeNamespace)
	})
	return cubeNamespace
}
