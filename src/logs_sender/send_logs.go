package logs_sender

import (
	"fmt"
	"os"
	"path"

	"github.com/openshift/assisted-installer-agent/src/config"
	"github.com/pkg/errors"

	"github.com/go-openapi/strfmt"
	"github.com/openshift/assisted-installer-agent/src/session"
	"github.com/openshift/assisted-installer-agent/src/util"
	"github.com/openshift/assisted-service/client/installer"

	log "github.com/sirupsen/logrus"
)

//go:generate mockery -name LogsSender -inpkg
type LogsSender interface {
	Execute(command string, args ...string) (stdout string, stderr string, exitCode int)
	ExecuteOutputToFile(outputFilePath string, command string, args ...string) (stderr string, exitCode int)
	CreateFolderIfNotExist(folder string) error
	FileUploader(filePath string, clusterID strfmt.UUID, hostID strfmt.UUID,
		inventoryUrl string, pullSecretToken string) error
}

type LogsSenderExecuter struct{}

func (e *LogsSenderExecuter) Execute(command string, args ...string) (stdout string, stderr string, exitCode int) {
	return util.Execute(command, args...)
}

func (e *LogsSenderExecuter) ExecuteOutputToFile(outputFilePath string, command string, args ...string) (stderr string, exitCode int) {
	return util.ExecuteOutputToFile(outputFilePath, command, args...)
}

func (e *LogsSenderExecuter) CreateFolderIfNotExist(folder string) error {
	if _, err := os.Stat(folder); os.IsNotExist(err) {
		return os.MkdirAll(folder, 0755)
	}
	return nil
}

func (e *LogsSenderExecuter) FileUploader(filePath string, clusterID strfmt.UUID, hostID strfmt.UUID,
	inventoryUrl string, pullSecretToken string) error {

	uploadFile, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer uploadFile.Close()

	invSession, err := session.New(inventoryUrl, pullSecretToken)
	if err != nil {
		log.Fatalf("Failed to initialize connection: %e", err)
	}

	params := installer.UploadHostLogsParams{
		Upfile:    uploadFile,
		ClusterID: clusterID,
		HostID:    hostID,
	}
	_, err = invSession.Client().Installer.UploadHostLogs(invSession.Context(), &params)

	return err
}

const logsDir = "/var/log"

var logsTmpFilesDir = path.Join(logsDir, "upload")

func getJournalLogsWithTag(l LogsSender, tag string, since string, outputFilePath string) error {
	log.Infof("Running journalctl with tag %s", tag)
	stderr, exitCode := l.ExecuteOutputToFile(outputFilePath, "journalctl", "-D", "/var/log/journal/",
		fmt.Sprintf("TAG=%s", tag), "--since", since, "--all")
	if exitCode != 0 {
		err := errors.Errorf(stderr)
		log.WithError(err).Errorf("Failed to run journalctl command")
		return err
	}
	return nil
}

func archiveFilesInFolder(l LogsSender, inputPath string, outputFile string) error {
	log.Infof("Archiving %s and creating %s", inputPath, outputFile)
	args := []string{"-czvf", outputFile, inputPath}

	_, err, execCode := l.Execute("tar", args...)

	if execCode != 0 {
		log.WithError(errors.Errorf(err)).Errorf("Failed to run to archive %s.", inputPath)
		return fmt.Errorf(err)
	}
	return nil
}

func uploadLogs(l LogsSender, filepath string, clusterID strfmt.UUID, hostId strfmt.UUID,
	inventoryUrl string, pullSecretToken string) error {

	err := l.FileUploader(filepath, clusterID, hostId, inventoryUrl, pullSecretToken)
	if err != nil {
		log.WithError(err).Errorf("Failed to upload file %s to assisted-service", filepath)
		return err
	}
	return nil
}

func SendLogs(l LogsSender) error {
	tags := config.LogsSenderConfig.Tags

	log.Infof("Start gathering journalctl logs with tags %s", tags)
	archivePath := fmt.Sprintf("%s/logs.tar.gz", logsDir)

	defer func() {
		if config.LogsSenderConfig.CleanWhenDone {
			_ = os.RemoveAll(logsTmpFilesDir)
			_ = os.Remove(archivePath)
		}
	}()
	if err := l.CreateFolderIfNotExist(logsTmpFilesDir); err != nil {
		log.WithError(err).Errorf("Failed to create directory %s", logsTmpFilesDir)
		return err
	}

	for _, tag := range tags {
		err := getJournalLogsWithTag(l, tag, config.LogsSenderConfig.Since, path.Join(logsTmpFilesDir, fmt.Sprintf("%s.logs", tag)))
		if err != nil {
			return err
		}
	}

	if err := archiveFilesInFolder(l, logsTmpFilesDir, archivePath); err != nil {
		return err
	}

	return uploadLogs(l, archivePath, strfmt.UUID(config.LogsSenderConfig.ClusterID),
		strfmt.UUID(config.LogsSenderConfig.HostID),
		config.LogsSenderConfig.TargetURL, config.LogsSenderConfig.PullSecretToken)
}
