/*
 * Tencent is pleased to support the open source community by making BK-CI 蓝鲸持续集成平台 available.
 *
 * Copyright (C) 2019 THL A29 Limited, a Tencent company.  All rights reserved.
 *
 * BK-CI 蓝鲸持续集成平台 is licensed under the MIT license.
 *
 * A copy of the MIT License is included in this file.
 *
 *
 * Terms of the MIT License:
 * ---------------------------------------------------
 * Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
 * documentation files (the "Software"), to deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all copies or substantial portions of
 * the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT
 * LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN
 * NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
 * WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 */

package job

import (
	"encoding/json"
	"fmt"
	"github.com/Tencent/bk-ci/src/agent/src/pkg/api"
	"github.com/Tencent/bk-ci/src/agent/src/pkg/logs"
	"github.com/Tencent/bk-ci/src/agent/src/pkg/util/fileutil"
	"github.com/Tencent/bk-ci/src/agent/src/pkg/util/systemutil"
	"os"
	"sync"
)

// buildManager 当前构建对象管理
type buildManager struct {
	// Lock 多协诚修改时的执行锁
	Lock sync.Mutex
	// preInstance 接取的构建任务但还没开始进行构建 [string]bool
	preInstances sync.Map
	// instances 正在执行中的构建对象 [int]*api.ThirdPartyBuildInfo
	instances sync.Map
}

var GBuildManager *buildManager

func init() {
	GBuildManager = new(buildManager)
}

func (b *buildManager) GetInstanceCount() int {
	var i = 0
	b.instances.Range(func(key, value interface{}) bool {
		i++
		return true
	})
	return i
}

func (b *buildManager) GetInstances() []api.ThirdPartyBuildInfo {
	result := make([]api.ThirdPartyBuildInfo, 0)
	b.instances.Range(func(key, value interface{}) bool {
		result = append(result, *value.(*api.ThirdPartyBuildInfo))
		return true
	})
	return result
}

func (b *buildManager) AddBuild(processId int, buildInfo *api.ThirdPartyBuildInfo) {
	bytes, _ := json.Marshal(buildInfo)
	logs.Info("add build: processId: ", processId, ", buildInfo: ", string(bytes))
	b.instances.Store(processId, buildInfo)
	// 启动构建了就删除preInstance
	b.preInstances.Delete(buildInfo.BuildId)

	// #5806 预先录入异常信息，在构建进程正常结束时清理掉。如果没清理掉，则说明进程非正常退出，可能被OS或人为杀死
	errorMsgFile := getWorkerErrorMsgFile(buildInfo.BuildId, buildInfo.VmSeqId)
	_ = fileutil.WriteString(errorMsgFile,
		"业务构建进程异常退出，可能被操作系统或其他程序杀掉，需自查并降低负载后重试，"+
			"或解压 agent.zip 还原安装后重启agent再重试。(Builder process was killed.)")
	_ = systemutil.Chmod(errorMsgFile, os.ModePerm)
	go b.waitProcessDone(processId)
}

func (b *buildManager) waitProcessDone(processId int) {
	process, err := os.FindProcess(processId)
	inf, ok := b.instances.Load(processId)
	var info *api.ThirdPartyBuildInfo
	if ok {
		info = inf.(*api.ThirdPartyBuildInfo)
	}
	if err != nil {
		errMsg := fmt.Sprintf("build process err, pid: %d, err: %s", processId, err.Error())
		logs.Warn(errMsg)
		b.instances.Delete(processId)
		workerBuildFinish(&api.ThirdPartyBuildWithStatus{ThirdPartyBuildInfo: *info, Message: errMsg})
		return
	}

	state, err := process.Wait()
	// #5806 从b-xxxx_build_msg.log 读取错误信息，此信息可由worker-agent.jar写入，用于当异常时能够将信息上报给服务器
	msgFile := getWorkerErrorMsgFile(info.BuildId, info.VmSeqId)
	msg, _ := fileutil.GetString(msgFile)
	logs.Info(fmt.Sprintf("build[%s] pid[%d] finish, state=%v err=%v, msg=%s", info.BuildId, processId, state, err, msg))

	if err != nil {
		if len(msg) == 0 {
			msg = err.Error()
		}
	}
	success := true
	if len(msg) == 0 {
		msg = fmt.Sprintf("worker pid[%d] exit", processId)
	} else {
		success = false
	}

	buildInfo := info
	b.instances.Delete(processId)
	workerBuildFinish(&api.ThirdPartyBuildWithStatus{ThirdPartyBuildInfo: *buildInfo, Success: success, Message: msg})
}

func (b *buildManager) GetPreInstancesCount() int {
	var i = 0
	b.preInstances.Range(func(key, value interface{}) bool {
		i++
		return true
	})
	return i
}

func (b *buildManager) AddPreInstance(buildId string) {
	b.preInstances.Store(buildId, true)
}
