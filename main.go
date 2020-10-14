package main

import (
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/color"
	git "github.com/libgit2/git2go/v30"
	"github.com/magiconair/properties"
)

const BuildInfoPathFromSourceRoot = "analytics/server/target/apptegic/WEB-INF/classes/ServerBuildInfo.properties"
const PomFile = "pom.xml"
const BuildNumber = "build.number"

var logger = log.New(os.Stdout, ">> ", 0)

func main() {
	start := time.Now()
	sourceRoot, present := os.LookupEnv("EVERGAGE_SOURCE_ROOT")
	if !present {
		logger.Fatal("EVERGAGE_SOURCE_ROOT is not set. Please set and try again")
	}

	repository, err := git.OpenRepository(sourceRoot)
	if err != nil {
		logger.Fatal("cannot open repository", err)
	}

	buildInfo, err := lastBuildInfo(sourceRoot)
	if err != nil {
		logger.Println("mvn clean install -DskipTests=true -P full", err)
		return
	}
	fmt.Printf("%s ServerBuildInfo.properties %s\n", strings.Repeat("#", 20), strings.Repeat("#", 20))
	fmt.Print(buildInfo)
	fmt.Printf("%s\n", strings.Repeat("#", 70))

	currentBranch := currentBranch(repository)
	latestCommit := currentBranch.Target().String()

	oldBuildCommit, ok := buildInfo.Get(BuildNumber)
	if !ok {
		logFatal(fmt.Errorf("cannot find build.number in %s", BuildInfoPathFromSourceRoot))
	}

	skipDiffCheck := false
	if oldBuildCommit == latestCommit[:7] {
		logger.Printf("a last build already exists with same commit [%s]. checking for unstaged files", oldBuildCommit)
		skipDiffCheck = true
	}

	var diffModulePomFilesMap map[string]interface{}
	if !skipDiffCheck {
		logger.Printf("existing build found, comparing diffs between [%s] and [%s]\n", oldBuildCommit, latestCommit[:7])

		oldBuildTree, err := getTree(oldBuildCommit, repository)
		logFatal(err)
		newTree, err := getTree(latestCommit, repository)
		logFatal(err)

		options, _ := git.DefaultDiffOptions()
		diff, err := repository.DiffTreeToTree(oldBuildTree, newTree, &options)
		logFatal(err)

		diffModulePomFilesMap = diffModulePomFiles(diff, sourceRoot)
	}

	logger.Printf("checking unstaged files locally")
	uncommitedModulePomFiles := uncommittedModulePomFiles(repository, sourceRoot)
	if len(uncommitedModulePomFiles) == 0 {
		logger.Println("no unstaged files found locally")
	}
	var modules []string
	allPomFiles := mergeMaps(diffModulePomFilesMap, uncommitedModulePomFiles)
	for pomFile := range allPomFiles {
		mavenModuleName := getMavenModuleName(pomFile)
		modules = append(modules, mavenModuleName)
	}
	if len(modules) == 0 {
		logger.Println("no changes detected. You may start server as is.")
		os.Exit(0)
	}
	logger.Printf("changed modules are %s. Completed in %v", modules, time.Since(start))
	for i, module := range modules {
		modules[i] = ":" + module
	}
	_, _ = color.Set(color.Green)
	modulesToBuild := strings.Join(modules, ",")
	logger.Printf("mvn --projects %s --also-make-dependents clean install -DskipTests\n", modulesToBuild)
	_, _ = color.Reset()

	logger.Println("run the above command? (y/n)")
	var yesOrNo string
	_, err = fmt.Scanln(&yesOrNo)
	logFatal(err)
	switch yesOrNo {
	case "y", "yes":
		cmd := exec.Command("mvn", "--projects", modulesToBuild, "--also-make-dependents", "clean", "install", "-DskipTests")
		cmd.Dir = sourceRoot
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		logFatal(err)
	case "n", "no":
		os.Exit(0)
	default:
		logger.Println("answer not recognised. existing....")
		os.Exit(0)
	}
}

func mergeMaps(map1 map[string]interface{}, map2 map[string]interface{}) map[string]interface{} {
	if len(map1) == 0 {
		return map2
	}
	if len(map2) == 0 {
		return map1
	}
	for k, v := range map1 {
		map2[k] = v
	}
	return map2
}

func diffModulePomFiles(diff *git.Diff, sourceRoot string) map[string]interface{} {
	var filesChanged []string
	var noOfFilesChanged int
	err := diff.ForEach(func(delta git.DiffDelta, progress float64) (git.DiffForEachHunkCallback, error) {
		fmt.Printf("[%s] %s\n", trim(delta.Status.String()), delta.NewFile.Path)
		filesChanged = append(filesChanged, delta.NewFile.Path)
		noOfFilesChanged++
		return nil, nil
	}, git.DiffDetailFiles)
	logger.Println("total number of files changed are" , noOfFilesChanged)
	logFatal(err)

	return getPomFiles(filesChanged, sourceRoot)
}

func uncommittedModulePomFiles(repository *git.Repository, sourceRoot string) map[string]interface{} {
	list, err := repository.StatusList(&git.StatusOptions{
		Show:     git.StatusShowIndexAndWorkdir,
		Flags:    git.StatusOptIncludeUntracked,
		Pathspec: nil,
	})
	logFatal(err)
	count, err := list.EntryCount()
	logFatal(err)

	var files []string
	for i := 0; i < count; i++ {
		index, err := list.ByIndex(i)
		logFatal(err)
		switch index.Status {
		case git.StatusWtModified, git.StatusWtDeleted, git.StatusWtTypeChange, git.StatusWtRenamed, git.StatusWtNew:
			fmt.Printf("[%s] %s\n", trim(index.IndexToWorkdir.Status.String()), index.IndexToWorkdir.NewFile.Path)
			files = append(files, index.IndexToWorkdir.NewFile.Path)
		case git.StatusIndexNew, git.StatusIndexModified, git.StatusIndexDeleted, git.StatusIndexRenamed, git.StatusIndexTypeChange:
			fmt.Printf("[%s] %s\n", trim(index.HeadToIndex.Status.String()), index.HeadToIndex.NewFile.Path)
			files = append(files, index.HeadToIndex.NewFile.Path)
		}
	}

	return getPomFiles(files, sourceRoot)
}

func getPomFiles(files []string, sourceRoot string) map[string]interface{} {
	pomFileMap := make(map[string]interface{})
	for _, file := range files {
		pomFile := getPomFile(sourceRoot, file)
		if pomFile == "" {
			_, _ = color.Set(color.Yellow)
			logger.Printf("ignoring [%s] since its not part of mvn src/", file)
			_, _ = color.Reset()
			continue
		}
		pomFileMap[pomFile] = struct{}{}
	}
	return pomFileMap
}

func getTree(commit string, repository *git.Repository) (*git.Tree, error) {
	switch {
	case len(commit) == 7:
		obj, err := repository.RevparseSingle(commit)
		if err != nil {
			return nil, err
		}
		commit, err := obj.AsCommit()
		if err != nil {
			return nil, err
		}
		tree, err := commit.Tree()
		if err != nil {
			return nil, err
		}
		return tree, nil
	case len(commit) == 40:
		oid, err := git.NewOid(commit)
		if err != nil {
			return nil, err
		}

		lookupCommit, err := repository.LookupCommit(oid)
		if err != nil {
			return nil, err
		}
		tree, err := lookupCommit.Tree()
		if err != nil {
			return nil, err
		}
		return tree, nil
	default:
		return nil, fmt.Errorf("commit [%s] is an invalid commit", commit)
	}
}

func getPomFile(repo, path string) string {
	if strings.Contains(path, PomFile) {
		return path
	}
	indexOfSrc := strings.Index(path, "/src/")
	if indexOfSrc == -1 {
		// Only track /src/ directories. others like docs/design should be ignored.
		return ""
	}
	relativeDir := path[:indexOfSrc]
	pfd := filepath.Join(repo, relativeDir)
	dir, err := ioutil.ReadDir(pfd)
	logFatal(err)
	for _, info := range dir {
		if info.Name() == PomFile {
			return filepath.Join(pfd, PomFile)
		}
	}
	return ""
}

func logFatal(err error) {
	if err != nil {
		logger.Fatal(err)
	}
}

func getMavenModuleName(pomFile string) string {
	mavenProject := struct {
		ArtifactId string `xml:"artifactId"`
	}{}
	pomFileRaw, err := ioutil.ReadFile(pomFile)
	logFatal(err)
	err = xml.Unmarshal(pomFileRaw, &mavenProject)
	logFatal(err)
	return mavenProject.ArtifactId
}

func lastBuildInfo(sourceRoot string) (*properties.Properties, error) {
	buildPrintln := filepath.Join(sourceRoot, BuildInfoPathFromSourceRoot)
	f, err := os.Open(buildPrintln)
	if err != nil {
		return nil, err
	}
	b, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}
	props, err := properties.Load(b, properties.UTF8)
	if err != nil {
		return nil, err
	}
	return props, nil
}

func currentBranch(repository *git.Repository) *git.Branch {
	head, err := repository.Head()
	logFatal(err)
	return head.Branch()
}

func trim(anystring string) string {
	return anystring[:3]
}
