package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/alexmullins/zip"
	cp "github.com/otiai10/copy"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sealdice-core/dice"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var binPrefix = "https://sealdice.coding.net/p/sealdice/d/sealdice-binaries/git/raw/master"

func downloadUpdate(dm *dice.DiceManager) error {
	if dm.AppVersionOnline != nil {
		ver := dm.AppVersionOnline
		if ver.VersionLatestCode != dm.AppVersionCode {
			platform := runtime.GOOS
			arch := runtime.GOARCH
			version := ver.VersionLatest
			var ext string

			switch platform {
			case "windows":
				ext = "zip"
			default:
				// 其他各种平台似乎都是 .tar.gz
				ext = "tar.gz"
			}

			if arch == "386" {
				arch = "i386"
			}

			fn := fmt.Sprintf("sealdice-core_%s_%s_%s.%s", version, platform, arch, ext)
			fileUrl := binPrefix + "/" + fn

			logger.Infof("准备下载更新: %s", fn)
			err := os.RemoveAll("./update")
			if err != nil {
				return errors.New("更新: 删除缓存目录(update)失败")
			}

			os.MkdirAll("./update", 0755)
			os.MkdirAll("./update/new", 0755)
			fn2 := "./update/update." + ext
			err = DownloadFile(fn2, fileUrl)
			if err != nil {
				fmt.Println("！！！", err)
				return errors.New("更新: 下载更新文件失败")
			}
			logger.Infof("更新下载完成，保存于: %s", fn2)

			if ext == "zip" {
				err = unzipSource(fn2, "./update/new")
				if err != nil {
					return errors.New("更新: 更新文件解压失败")
				}
			} else {
				err = ExtractTarGz(fn2, "./update/new")
				if err != nil {
					return errors.New("更新: 更新文件解压失败")
				}
			}
		}
	}
	return nil
}

func RebootRequestListen(dm *dice.DiceManager) {
	<-dm.RebootRequestChan
	doReboot(dm)
}

func UpdateCheckRequestListen(dm *dice.DiceManager) {
	for {
		<-dm.UpdateCheckRequestChan
		CheckVersion(dm)
	}
}

func UpdateRequestListen(dm *dice.DiceManager) {
	<-dm.UpdateRequestChan
	err := downloadUpdate(dm)
	if err == nil {
		dm.UpdateDownloadedChan <- ""
		time.Sleep(2 * time.Second)
		doUpdate(dm)
		doReboot(dm)
	} else {
		dm.UpdateDownloadedChan <- err.Error()
	}
}

func doReboot(dm *dice.DiceManager) {
	executablePath, err := filepath.Abs(os.Args[0])
	if err != nil {
		return
	}

	binary, err := exec.LookPath(executablePath)
	if err != nil {
		logger.Errorf("Restart Error: %s", err)
		return
	}
	platform := runtime.GOOS
	if platform == "windows" {
		_ = exec.Command(binary, "--delay=10").Start()
	} else {
		// 手动cleanup
		cleanUpCreate(dm)()
		// os.Args[1:]...
		execErr := syscall.Exec(binary, []string{os.Args[0], "--delay=25"}, os.Environ())
		if execErr != nil {
			logger.Errorf("Restart error: %s %v", binary, execErr)
		}
	}
	os.Exit(0)
}

func doUpdate(dm *dice.DiceManager) {
	platform := runtime.GOOS
	if platform == "windows" {
		exe, err := filepath.Abs(os.Args[0])
		if err == nil {
			cp.Copy(exe, "./auto_update.exe")
		}
	} else {
		// 好像gocq那边有点问题，也采取和windows一样的模式好了
		// 放置标记物（实际没有用，带参数重启）
		exe, err := filepath.Abs(os.Args[0])
		if err == nil {
			os.Rename(exe, "./auto_update")
			cp.Copy("./update/new/sealdice-core", exe)
			_ = os.Chmod(exe, 0755)
		}

		//err := cp.Copy("./update/new", "./")
		//if err != nil {
		//	logger.Errorf("更新: 复制文件失败: %s", err.Error())
		//}
		//_ = os.Chmod("./sealdice-core", 0755)
		//_ = os.Chmod("./go-cqhttp/go-cqhttp", 0755)
	}
}

func CheckVersion(dm *dice.DiceManager) *dice.VersionInfo {
	resp, err := http.Get("https://dice.weizaima.com/dice/api/version?versionCode=" + strconv.FormatInt(dm.AppVersionCode, 10))
	if err != nil {
		logger.Errorf("获取新版本失败: %s", err.Error())
		return nil
	}
	defer resp.Body.Close()

	var ver dice.VersionInfo
	err = json.NewDecoder(resp.Body).Decode(&ver)
	if err != nil {
		return nil
	}

	dm.AppVersionOnline = &ver
	//downloadUpdate(dm)
	return &ver
}

func DownloadFile(filepath string, url string) error {
	// Get the data
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Create the file
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	return err
}

func unzipSource(source, destination string) error {
	// 1. Open the zip file
	reader, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer reader.Close()

	// 2. Get the absolute destination path
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}

	// 3. Iterate over zip files inside the archive and unzip each of them
	for _, f := range reader.File {
		err := unzipFile(f, destination)
		if err != nil {
			return err
		}
	}

	return nil
}

func unzipFile(f *zip.File, destination string) error {
	// 4. Check if file paths are not vulnerable to Zip Slip
	filePath := filepath.Join(destination, f.Name)
	if !strings.HasPrefix(filePath, filepath.Clean(destination)+string(os.PathSeparator)) {
		return fmt.Errorf("invalid file path: %s", filePath)
	}

	// 5. Create directory tree
	if f.FileInfo().IsDir() {
		if err := os.MkdirAll(filePath, os.ModePerm); err != nil {
			return err
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(filePath), os.ModePerm); err != nil {
		return err
	}

	// 6. Create a destination file for unzipped content
	destinationFile, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer destinationFile.Close()

	// 7. Unzip the content of a file and copy it to the destination file
	zippedFile, err := f.Open()
	if err != nil {
		return err
	}
	defer zippedFile.Close()

	if _, err := io.Copy(destinationFile, zippedFile); err != nil {
		return err
	}
	return nil
}

func ExtractTarGz(fn, dest string) error {
	gzipStream, err := os.Open(fn)
	if err != nil {
		fmt.Println("error", err.Error())
		return err
	}
	defer gzipStream.Close()

	log := logger
	uncompressedStream, err := gzip.NewReader(gzipStream)
	if err != nil {
		log.Fatal("ExtractTarGz: NewReader failed")
		return err
	}
	defer uncompressedStream.Close()

	tarReader := tar.NewReader(uncompressedStream)

	for true {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			log.Fatalf("ExtractTarGz: Next() failed: %s", err.Error())
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(filepath.Join(dest, header.Name), 0755); err != nil {
				log.Fatalf("ExtractTarGz: Mkdir() failed: %s", err.Error())
			}
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(filepath.Join(dest, header.Name)), 0755) // 进行一个目录的创
			outFile, err := os.Create(filepath.Join(dest, header.Name))
			if err != nil {
				log.Fatalf("ExtractTarGz: Create() failed: %s", err.Error())
				return err
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				log.Fatalf("ExtractTarGz: Copy() failed: %s", err.Error())
				return err
			}
			outFile.Close()

		default:
			log.Fatalf(
				"ExtractTarGz: uknown type: %s in %s",
				header.Typeflag,
				header.Name)
			return err
		}
	}
	return nil
}
