#!/bin/bash
#
# Copyright 2020 the original author or authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Example usage on Mac OS: DOCKER_HOST_IP=127.0.0.1 bash load_mib_template.sh

ETCD_IP=${DOCKER_HOST_IP:=172.17.0.1}

set -x
cat BBSM-12345123451234512345-00000000000001-v1.json| ETCDCTL_API=3 etcdctl --endpoints=$ETCD_IP:2379 put service/voltha/omci_mibs/go_templates/BBSM/12345123451234512345/00000000000001
