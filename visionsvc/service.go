package visionsvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"os"
	"slices"
	"sort"
	"sync"

	"github.com/pkg/errors"
	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/services/vision"
	"go.viam.com/rdk/utils"
	vis "go.viam.com/rdk/vision"
	"go.viam.com/rdk/vision/classification"
	"go.viam.com/rdk/vision/objectdetection"
)

var errUnimplemented = errors.New("unimplemented")
var Model = resource.NewModel("viam-soleng", "vision", "detect-and-classify")
var PrettyName = "Viam detect and classify vision service"
var Description = "A module of the Viam vision service that crops an image to an initial detection then runs other models to return detections"

type Config struct {
	Camera             string   `json:"camera"`
	Detector           string   `json:"detector"`
	DetectorConfidence float64  `json:"detector_confidence"`
	MaxDetections      int      `json:"max_detections"`
	DetectorLabels     []string `json:"detector_labels"`
	DetPadding         int      `json:"padding"`
	Classifier1        string   `json:"classifier1"`
	Classifier2        string   `json:"classifier2"`
	MaxClassifications int      `json:"max_classifications"`
	LogImage           bool     `json:"log_image"`
	ImagePath          string   `json:"image_path"`

	// TODO: Implement list of classifiers -> https://github.com/viamrobotics/rdk/blob/main/components/camera/transformpipeline/transform.go#L110
	Pipeline []Classification `json:"classifiers"`
}

// Classification configuration type.
type Classification struct {
	Classifier string             `json:"classifier"`
	Attributes utils.AttributeMap `json:"attributes"`
}
type myVisionSvc struct {
	resource.Named
	logger             logging.Logger
	camera             camera.Camera
	detector           vision.Service
	detectorConfidence float64
	maxDetections      int
	detectorLabels     []string
	detPadding         int
	classifier         vision.Service
	classifier2        vision.Service
	maxClassifications int
	logImage           bool
	imagePath          string
	mu                 sync.RWMutex
	cancelCtx          context.Context
	cancelFunc         func()
	done               chan bool
}

func init() {
	resource.RegisterService(
		vision.API,
		Model,
		resource.Registration[vision.Service, *Config]{
			Constructor: newService,
		})
}

func newService(ctx context.Context, deps resource.Dependencies, conf resource.Config, logger logging.Logger) (vision.Service, error) {
	logger.Debugf("Starting %s %s", PrettyName)
	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	svc := myVisionSvc{
		Named:      conf.ResourceName().AsNamed(),
		logger:     logger,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
		mu:         sync.RWMutex{},
		done:       make(chan bool),
	}

	if err := svc.Reconfigure(ctx, deps, conf); err != nil {
		return nil, err
	}
	return &svc, nil
}

func (cfg *Config) Validate(path string) ([]string, error) {
	if cfg.Camera == "" {
		return nil, errors.New("camera is required")
	}
	if cfg.Detector == "" {
		return nil, errors.New("detector is required")
	}
	if cfg.DetectorConfidence <= 0.0 {
		return nil, errors.New("detector_confidence must be >= 0.0")
	}
	if cfg.Classifier1 == "" {
		return nil, errors.New("classifier1_service is required")
	}
	if cfg.Classifier2 == "" {
		return nil, errors.New("classifier2_service is required")
	}
	/* TODO: Deactivated until list of classifier is implemented
	if cfg.MaxClassifications == 0 {
		return nil, errors.New("max_classifications must be > 0")
	}
	*/
	return []string{cfg.Camera, cfg.Detector, cfg.Classifier1, cfg.Classifier2}, nil
}

// Reconfigure reconfigures with new settings.
func (svc *myVisionSvc) Reconfigure(ctx context.Context, deps resource.Dependencies, conf resource.Config) error {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.logger.Debugf("Reconfiguring %s", PrettyName)
	// In case the module has changed name
	svc.Named = conf.ResourceName().AsNamed()
	newConf, err := resource.NativeConfig[*Config](conf)
	if err != nil {
		return err
	}
	// Get the camera
	svc.camera, err = camera.FromDependencies(deps, newConf.Camera)
	if err != nil {
		return errors.Wrapf(err, "unable to get source camera %v for image sourcing...", newConf.Detector)
	}
	// Get the face cropper
	svc.detector, err = vision.FromDependencies(deps, newConf.Detector)
	if err != nil {
		return errors.Wrapf(err, "unable to get Object Detector %v for image cropping...", newConf.Detector)
	}
	// Get the detector confidence threshold
	svc.detectorConfidence = newConf.DetectorConfidence
	// Get the classifiers
	svc.classifier, err = vision.FromDependencies(deps, newConf.Classifier1)
	if err != nil {
		return errors.Wrapf(err, "unable to get classifier %v ", newConf.Classifier1)
	}
	svc.classifier2, err = vision.FromDependencies(deps, newConf.Classifier2)
	if err != nil {
		return errors.Wrapf(err, "unable to get classifier2 %v ", newConf.Classifier2)
	}
	svc.detPadding = newConf.DetPadding
	svc.maxDetections = newConf.MaxDetections
	svc.detectorLabels = newConf.DetectorLabels
	svc.logImage = newConf.LogImage
	svc.imagePath = newConf.ImagePath
	svc.maxClassifications = newConf.MaxClassifications
	svc.logger.Debug("**** Reconfigured ****")
	return nil
}

// Classifications can be implemented to extend functionality but returns unimplemented currently.
func (svc *myVisionSvc) Classifications(ctx context.Context, img image.Image, n int, extra map[string]interface{}) (classification.Classifications, error) {
	return svc.detectAndClassify(ctx, img)
}

// ClassificationsFromCamera can be implemented to extend functionality but returns unimplemented currently.
func (svc *myVisionSvc) ClassificationsFromCamera(ctx context.Context, cameraName string, n int, extra map[string]interface{}) (classification.Classifications, error) {
	// gets the stream from a camera
	stream, err := svc.camera.Stream(context.Background())
	if err != nil {
		return nil, err
	}
	// gets an image from the camera stream
	img, release, err := stream.Next(context.Background())
	if err != nil {
		return nil, err
	}
	defer release()
	return svc.detectAndClassify(ctx, img)
}

func (svc *myVisionSvc) Detections(ctx context.Context, img image.Image, extra map[string]interface{}) ([]objectdetection.Detection, error) {
	return nil, errUnimplemented
}

func (svc *myVisionSvc) DetectionsFromCamera(ctx context.Context, camera string, extra map[string]interface{}) ([]objectdetection.Detection, error) {
	return nil, errUnimplemented
}

// ObjectPointClouds can be implemented to extend functionality but returns unimplemented currently.
func (s *myVisionSvc) GetObjectPointClouds(ctx context.Context, cameraName string, extra map[string]interface{}) ([]*vis.Object, error) {
	return nil, errUnimplemented
}

// DoCommand can be implemented to extend functionality but returns unimplemented currently.
func (s *myVisionSvc) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, errUnimplemented
}

// The close method is executed when the component is shut down
func (svc *myVisionSvc) Close(ctx context.Context) error {
	svc.logger.Debugf("Shutting down %s", PrettyName)
	svc.camera.Close(ctx)
	return nil
}

// Take an input image, detect objects, crop the image down to the detected bounding box and
// hand over to classifier for more accurate classifications
func (svc *myVisionSvc) detectAndClassify(ctx context.Context, img image.Image) (classification.Classifications, error) {
	// Get detections from the provided Image
	detections, err := svc.detector.Detections(ctx, img, nil)
	if err != nil {
		return nil, err
	}
	// sort detections based upon score
	sort.Slice(detections, func(i, j int) bool {
		return detections[i].Score() > detections[j].Score()
	})
	// trim detections based upon max detections setting / if detectorMaxDetections = 0 -> no limit
	if len(detections) > svc.maxDetections && svc.maxDetections != 0 {
		detections = detections[:svc.maxDetections]
	}
	svc.logger.Infof("Detections #: %v/%v", len(detections), svc.maxDetections)
	svc.logger.Debugf("Detections Details: %v", detections)
	// Result set to be returned
	var classificationResult classification.Classifications
	for _, detection := range detections {
		// Check if the detection score is above the configured threshold
		if detection.Score() >= svc.detectorConfidence && slices.Contains(svc.detectorLabels, detection.Label()) {
			// Increase/decrease bounding box according to detection border setting
			rectangle := image.Rect(
				detection.BoundingBox().Min.X-svc.detPadding,
				detection.BoundingBox().Min.Y-svc.detPadding,
				detection.BoundingBox().Max.X+svc.detPadding,
				detection.BoundingBox().Max.Y+svc.detPadding)
			croppedImg, err := cropImage(img, &rectangle)
			if err != nil {
				return nil, err
			}
			// Save cropped images to disk
			if svc.logImage {
				err := saveImage(croppedImg, svc.imagePath)
				if err != nil {
					return nil, err
				}
			}
			// Pass the cropped image to the classifier1 and get the classification with the highest confidence
			classification, err := svc.classifier.Classifications(ctx, croppedImg, 1, nil)
			if err != nil {
				return nil, err
			}
			classificationResult = append(classificationResult, classification...)
			// Pass the cropped image to the classifier2 and get the classification with the highest confidence
			classification2, err := svc.classifier2.Classifications(ctx, croppedImg, 1, nil)
			if err != nil {
				return nil, err
			}
			classificationResult = append(classificationResult, classification2...)
		}
	}
	sort.Slice(classificationResult, func(i, j int) bool {
		return classificationResult[i].Score() > classificationResult[j].Score()
	})
	if len(classificationResult) > svc.maxClassifications && svc.maxClassifications != 0 {
		classificationResult = classificationResult[:svc.maxClassifications]
	}
	return classificationResult, nil
}

func cropImage(img image.Image, rect *image.Rectangle) (image.Image, error) {
	// The cropping operation is done by creating a new image of the size of the rectangle
	// and drawing the relevant part of the original image onto the new image.
	cropped := image.NewRGBA(rect.Bounds())
	draw.Draw(cropped, rect.Bounds(), img, rect.Min, draw.Src)
	return cropped, nil
}

func saveImage(image image.Image, imagePath string) error {
	buf := new(bytes.Buffer)
	err := jpeg.Encode(buf, image, nil)
	if err != nil {
		return err
	}
	digest := sha256.New()
	digest.Write(buf.Bytes())
	hash := digest.Sum(nil)
	f, err := os.Create(fmt.Sprintf("%v/%x.jpg", imagePath, hash))
	if err != nil {
		return err
	}
	defer f.Close()
	opt := jpeg.Options{
		Quality: 90,
	}
	jpeg.Encode(f, image, &opt)
	return nil
}
