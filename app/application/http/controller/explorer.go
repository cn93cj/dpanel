package controller

import (
	"archive/tar"
	"archive/zip"
	"errors"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/archive"
	"github.com/donknap/dpanel/app/application/logic"
	"github.com/donknap/dpanel/common/function"
	"github.com/donknap/dpanel/common/service/docker"
	"github.com/donknap/dpanel/common/service/storage"
	"github.com/gin-gonic/gin"
	"github.com/h2non/filetype"
	"github.com/we7coreteam/w7-rangine-go/v2/src/http/controller"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Explorer struct {
	controller.Abstract
}

func (self Explorer) Export(http *gin.Context) {
	type ParamsValidate struct {
		Md5      string   `json:"md5" binding:"required"`
		FileList []string `json:"fileList" binding:"required"`
	}
	params := ParamsValidate{}
	if !self.Validate(http, &params) {
		return
	}
	var err error

	zipTempFile, _ := os.CreateTemp("", "dpanel")
	defer func() {
		_ = os.Remove(zipTempFile.Name())
	}()
	zipWriter := zip.NewWriter(zipTempFile)

	// 需要先将每个目录导出，然后再合并起来。直接导出整个容器效率太低
	for _, path := range params.FileList {
		out, _, err := docker.Sdk.Client.CopyFromContainer(docker.Sdk.Ctx, params.Md5, path)
		if err != nil {
			self.JsonResponseWithError(http, err, 500)
			return
		}
		tarReader := tar.NewReader(out)
		for {
			file, err := tarReader.Next()
			if err != nil {
				break
			}
			switch file.Typeflag {
			case tar.TypeReg, tar.TypeRegA, tar.TypeDir, tar.TypeGNUSparse:
				zipHeader := &zip.FileHeader{
					Name:               file.Name,
					Method:             zip.Deflate,
					UncompressedSize64: uint64(file.Size),
					Modified:           file.ModTime,
				}
				writer, _ := zipWriter.CreateHeader(zipHeader)
				_, _ = io.Copy(writer, tarReader)
			}
		}
		_ = out.Close()
	}
	err = zipWriter.Close()
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	http.Header("Content-Type", "application/zip")
	http.Header("Content-Disposition", "attachment; filename=export.zip")
	http.File(zipTempFile.Name())
	return
}

func (self Explorer) ImportFileContent(http *gin.Context) {
	type ParamsValidate struct {
		File     string `json:"file" binding:"required"`
		Content  string `json:"content"`
		Md5      string `json:"md5" binding:"required"`
		DestPath string `json:"destPath" binding:"required"`
	}
	params := ParamsValidate{}
	if !self.Validate(http, &params) {
		return
	}
	if !strings.HasPrefix(params.File, "/") || !strings.HasPrefix(params.DestPath, "/") {
		self.JsonResponseWithError(http, errors.New("请指定绝对路径"), 500)
		return
	}
	tempFileDir, _ := os.MkdirTemp("", "dpanel-explorer")
	tempFilePath := fmt.Sprintf("%s%s", strings.TrimSuffix(tempFileDir, "/"), params.File)
	os.MkdirAll(filepath.Dir(tempFilePath), os.ModePerm)
	fmt.Printf("%v \n", tempFilePath)

	err := os.WriteFile(tempFilePath, []byte(params.Content), 0o666)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	defer os.RemoveAll(tempFileDir)
	tarReader, err := archive.Tar(tempFileDir, archive.Uncompressed)
	err = docker.Sdk.Client.CopyToContainer(docker.Sdk.Ctx,
		params.Md5,
		params.DestPath,
		tarReader,
		container.CopyToContainerOptions{},
	)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	self.JsonSuccessResponse(http)
}

func (self Explorer) Import(http *gin.Context) {
	type ParamsValidate struct {
		Md5      string `json:"md5" binding:"required"`
		FileList []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"fileList" binding:"required"`
		DestPath string `json:"destPath" binding:"required"`
	}
	params := ParamsValidate{}
	if !self.Validate(http, &params) {
		return
	}
	_, err := docker.Sdk.Client.ContainerInspect(docker.Sdk.Ctx, params.Md5)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	uploadTempDir, _ := os.MkdirTemp("", "dpanel")
	defer os.RemoveAll(uploadTempDir)
	for _, item := range params.FileList {
		sourceFile, err := os.Open(storage.Local{}.GetRealPath(item.Path))
		if err != nil {
			self.JsonResponseWithError(http, err, 500)
			return
		}
		defer sourceFile.Close()
		os.Remove(sourceFile.Name())

		destFile, err := os.Create(uploadTempDir + "/" + item.Name)
		if err != nil {
			self.JsonResponseWithError(http, err, 500)
			return
		}
		defer destFile.Close()

		_, err = io.Copy(destFile, sourceFile)
		if err != nil {
			self.JsonResponseWithError(http, err, 500)
			return
		}
	}
	tarReader, err := archive.Tar(uploadTempDir, archive.Uncompressed)
	err = docker.Sdk.Client.CopyToContainer(docker.Sdk.Ctx,
		params.Md5,
		params.DestPath,
		tarReader,
		container.CopyToContainerOptions{},
	)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	self.JsonSuccessResponse(http)
	return
}

func (self Explorer) Unzip(http *gin.Context) {
	type ParamsValidate struct {
		Md5  string `json:"md5" binding:"required"`
		File string `json:"file" binding:"required"`
	}
	params := ParamsValidate{}
	if !self.Validate(http, &params) {
		return
	}
	explorer, err := logic.NewExplorer(params.Md5)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	err = explorer.Unzip(filepath.Dir(params.File), filepath.Base(params.File))
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	self.JsonSuccessResponse(http)
	return
}

func (self Explorer) Delete(http *gin.Context) {
	type ParamsValidate struct {
		Md5      string   `json:"md5" binding:"required"`
		FileList []string `json:"fileList" binding:"required"`
	}
	params := ParamsValidate{}
	if !self.Validate(http, &params) {
		return
	}

	for _, path := range params.FileList {
		if path == "/" ||
			path == "./" ||
			path == "." ||
			strings.Contains(path, "*") {
			self.JsonResponseWithError(http, errors.New("只可以删除指定的文件或是目录"), 500)
			return
		}
	}
	explorer, err := logic.NewExplorer(params.Md5)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	err = explorer.DeleteFileList(params.FileList)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	self.JsonSuccessResponse(http)
	return
}

func (self Explorer) GetPathList(http *gin.Context) {
	type ParamsValidate struct {
		Md5  string `json:"md5" binding:"required"`
		Path string `json:"path" binding:"required"`
	}
	params := ParamsValidate{}
	if !self.Validate(http, &params) {
		return
	}
	containerInfo, err := docker.Sdk.Client.ContainerInspect(docker.Sdk.Ctx, params.Md5)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	explorer, err := logic.NewExplorer(params.Md5)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	result, err := explorer.GetListByPath(params.Path)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	if function.IsEmptyArray(result) {
		self.JsonResponseWithoutError(http, gin.H{
			"list": result,
		})
		return
	}
	var tempChangeFileList = make(map[string]container.FilesystemChange)
	changeFileList, err := docker.Sdk.Client.ContainerDiff(docker.Sdk.Ctx, params.Md5)
	if !function.IsEmptyArray(changeFileList) {
		for _, change := range changeFileList {
			tempChangeFileList[change.Path] = change
		}
	}
	userList, err := explorer.GetPasswd()
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}

	for index, item := range result {
		if tempChangeFileList != nil {
			if change, ok := tempChangeFileList[item.Name]; ok {
				item.Change = int(change.Kind)
			}
		}
		if !function.IsEmptyArray(containerInfo.Mounts) {
			for _, mount := range containerInfo.Mounts {
				if strings.HasPrefix(item.Name, mount.Destination) {
					item.Change = 100
					break
				}
			}
		}
		for _, userItem := range userList {
			if userItem.UID == item.Owner {
				result[index].Owner = userItem.Username
				result[index].Group = userItem.Username
			}
		}
	}
	self.JsonResponseWithoutError(http, gin.H{
		"list": result,
	})
	return
}

func (self Explorer) GetContent(http *gin.Context) {
	type ParamsValidate struct {
		Md5  string `json:"md5" binding:"required"`
		File string `json:"file" binding:"required"`
	}
	params := ParamsValidate{}
	if !self.Validate(http, &params) {
		return
	}
	pathStat, err := docker.Sdk.Client.ContainerStatPath(docker.Sdk.Ctx, params.Md5, params.File)
	if pathStat.Size >= 1024*1024 {
		self.JsonResponseWithError(http, errors.New("超过1M的文件请通过导入&导出修改文件"), 500)
		return
	}
	out, _, err := docker.Sdk.Client.CopyFromContainer(docker.Sdk.Ctx, params.Md5, params.File)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	var content []byte
	tarReader := tar.NewReader(out)
	for {
		file, err := tarReader.Next()
		if err != nil {
			break
		}
		if file.Typeflag != tar.TypeReg {
			continue
		}
		if file.Name != filepath.Base(params.File) {
			continue
		}
		content = make([]byte, file.Size)
		tarReader.Read(content)
	}
	out.Close()
	if content == nil {
		self.JsonResponseWithError(http, errors.New("获取文件失败"), 500)
		return
	}
	fileType, _ := filetype.Match(content)
	if fileType == filetype.Unknown {
		self.JsonResponseWithoutError(http, gin.H{
			"content": string(content),
		})
		return
	} else {
		self.JsonResponseWithError(http, errors.New("文件类型不支持在线编辑"), 500)
		return
	}
}

func (self Explorer) Chmod(http *gin.Context) {
	type ParamsValidate struct {
		Md5         string   `json:"md5" binding:"required"`
		FileList    []string `json:"fileList" binding:"required"`
		Mod         int      `json:"mod" binding:"required"`
		HasChildren bool     `json:"hasChildren"`
		Owner       string   `json:"owner"`
	}
	params := ParamsValidate{}
	if !self.Validate(http, &params) {
		return
	}
	explorer, err := logic.NewExplorer(params.Md5)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	err = explorer.Chmod(params.FileList, params.Mod, params.HasChildren)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	if params.Owner != "" {
		err = explorer.Chown(params.Md5, params.FileList, params.Owner, params.HasChildren)
		if err != nil {
			self.JsonResponseWithError(http, err, 500)
			return
		}
	}

	self.JsonSuccessResponse(http)
	return
}

func (self Explorer) GetPasswd(http *gin.Context) {
	type ParamsValidate struct {
		Md5 string `json:"md5" binding:"required"`
	}
	params := ParamsValidate{}
	if !self.Validate(http, &params) {
		return
	}
	explorer, err := logic.NewExplorer(params.Md5)
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	userList, err := explorer.GetPasswd()
	if err != nil {
		self.JsonResponseWithError(http, err, 500)
		return
	}
	self.JsonResponseWithoutError(http, gin.H{
		"list": userList,
	})
	return
}
